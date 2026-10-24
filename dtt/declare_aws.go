package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// This file is the aws provider (see declare.go): ECS tasks and their tags,
// through the AWS CLI, so credentials and region come from wherever the CLI
// finds them.
//
//	aws.accounts.current.ecs.tasks:        (or aws.accounts.<id>..., or
//	                                        aws.accounts.current.regions.<region>.ecs.tasks)
//	  select:
//	    cluster: <name or ARN>             (default: every cluster)
//	    tags:
//	      watched-by: dtt                  (* for any value)
//	  assert:
//	    tags:
//	      last-watched: ${{timestamp.unix}}
//
// Every task whose tags match is one object; each asserted tag is a field
// (tags.<key>). $DTT_AWS_CLI names the CLI if it isn't "aws" on PATH.
type awsProvider struct{}

// awsRunner runs one AWS CLI command, returning its JSON output.
type awsRunner func(ctx context.Context, region string, args ...string) ([]byte, error)

var awsAccountRe = regexp.MustCompile(`^[0-9]{12}$`)

type awsTask struct {
	arn, cluster, region string
}

func awsPath(path []string) (account, region string, err error) {
	switch {
	case len(path) == 5 && path[1] == "accounts" && path[3] == "ecs" && path[4] == "tasks":
		account = path[2]
	case len(path) == 7 && path[1] == "accounts" && path[3] == "regions" && path[5] == "ecs" && path[6] == "tasks":
		account, region = path[2], path[4]
	default:
		return "", "", fmt.Errorf("aws paths are aws.accounts.<current|id>.ecs.tasks (or aws.accounts.<current|id>.regions.<region>.ecs.tasks)")
	}
	if account != "current" && !awsAccountRe.MatchString(account) {
		return "", "", fmt.Errorf("account %q should be current or a 12-digit account id", account)
	}
	return account, region, nil
}

func (awsProvider) checkSelect(st *statement) error {
	if _, _, err := awsPath(st.path); err != nil {
		return err
	}
	if st.sel == nil {
		return nil
	}
	if st.sel.kind != yMap {
		return fmt.Errorf("select ECS tasks by tags (and cluster)")
	}
	if err := onlyKeys(st.sel, "ECS tasks' select", "cluster", "tags"); err != nil {
		return err
	}
	if _, err := scalar(st.sel, "cluster"); err != nil {
		return err
	}
	if tags := st.sel.get("tags"); tags != nil {
		if tags.kind != yMap {
			return fmt.Errorf("line %d: select.tags should be a mapping of key: value", tags.line)
		}
		for i, v := range tags.vals {
			if v.kind != yScalar {
				return fmt.Errorf("line %d: tag %s should be a single value", v.line, tags.keys[i])
			}
		}
	}
	return nil
}

func (awsProvider) checkAssert(_ []string, body *yNode) error {
	_, err := parseAWSAssert(body)
	return err
}

func parseAWSAssert(body *yNode) ([]fieldSpec, error) {
	if body.kind != yMap {
		return nil, fmt.Errorf("line %d: an ECS task's assert is a mapping with tags under it", body.line)
	}
	if err := onlyKeys(body, "an ECS task's assert", "tags"); err != nil {
		return nil, err
	}
	fields, err := assertFields(body.get("tags"), "tags")
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		if f.want.kind != yScalar {
			return nil, fmt.Errorf("line %d: tag %s should be a single value", f.line, f.name)
		}
	}
	return fields, nil
}

func (awsProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	account, region, _ := awsPath(st.path)
	if account != "current" {
		out, err := e.awsRun(ctx, region, "sts", "get-caller-identity")
		if err != nil {
			return nil, err
		}
		var id struct{ Account string }
		if err := json.Unmarshal(out, &id); err != nil {
			return nil, fmt.Errorf("aws sts get-caller-identity: %v", err)
		}
		if id.Account != account {
			return nil, fmt.Errorf("the AWS credentials here are for account %s, not %s", id.Account, account)
		}
	}
	want := map[string]string{}
	var clusters []string
	if st.sel != nil {
		if tags := st.sel.get("tags"); tags != nil {
			for i, k := range tags.keys {
				v, err := e.text(tags.vals[i], "select.tags."+k)
				if err != nil {
					return nil, err
				}
				want[k] = v
			}
		}
		if c := st.sel.get("cluster"); c != nil && !isEmpty(c) {
			name, err := e.text(c, "select.cluster")
			if err != nil {
				return nil, err
			}
			clusters = []string{name}
		}
	}
	if clusters == nil {
		out, err := e.awsRun(ctx, region, "ecs", "list-clusters")
		if err != nil {
			return nil, err
		}
		var list struct{ ClusterArns []string }
		if err := json.Unmarshal(out, &list); err != nil {
			return nil, fmt.Errorf("aws ecs list-clusters: %v", err)
		}
		clusters = list.ClusterArns
	}

	base := st.pathString()
	var objs []*dobj
	for _, cluster := range clusters {
		out, err := e.awsRun(ctx, region, "ecs", "list-tasks", "--cluster", cluster)
		if err != nil {
			return nil, err
		}
		var list struct{ TaskArns []string }
		if err := json.Unmarshal(out, &list); err != nil {
			return nil, fmt.Errorf("aws ecs list-tasks: %v", err)
		}
		for start := 0; start < len(list.TaskArns); start += 100 {
			batch := list.TaskArns[start:min(start+100, len(list.TaskArns))]
			args := append([]string{"ecs", "describe-tasks", "--cluster", cluster, "--include", "TAGS", "--tasks"}, batch...)
			out, err := e.awsRun(ctx, region, args...)
			if err != nil {
				return nil, err
			}
			var desc struct {
				Tasks []struct {
					TaskArn           string
					ClusterArn        string
					LastStatus        string
					TaskDefinitionArn string
					Group             string
					Tags              []struct{ Key, Value string }
				}
			}
			if err := json.Unmarshal(out, &desc); err != nil {
				return nil, fmt.Errorf("aws ecs describe-tasks: %v", err)
			}
			for _, t := range desc.Tasks {
				tags := map[string]any{}
				for _, kv := range t.Tags {
					tags[kv.Key] = kv.Value
				}
				if !tagsMatch(tags, want) {
					continue
				}
				state := map[string]any{"arn": t.TaskArn, "cluster": t.ClusterArn, "last_status": t.LastStatus,
					"task_definition": t.TaskDefinitionArn, "group": t.Group, "tags": tags}
				objs = append(objs, e.addObject(base+"["+t.TaskArn+"]", base, st.line, state, &awsTask{arn: t.TaskArn, cluster: cluster, region: region}))
			}
		}
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "aws: %d ECS task(s) in %d cluster(s) match %s's select\n", len(objs), len(clusters), base)
	}
	return objs, nil
}

func tagsMatch(have map[string]any, want map[string]string) bool {
	for _, k := range sortedKeys(want) {
		v, ok := have[k]
		if !ok || (want[k] != "*" && v != want[k]) {
			return false
		}
	}
	return true
}

func (awsProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	fields, err := parseAWSAssert(body)
	if err != nil {
		return err
	}
	t := o.handle.(*awsTask)
	tags, _ := o.img.State["tags"].(map[string]any)
	if tags == nil {
		tags = map[string]any{}
		o.img.State["tags"] = tags
	}
	type tag struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	var set []tag
	for _, f := range fields {
		want, err := e.text(f.want, "tags."+f.name)
		if err != nil {
			return err
		}
		from, err := e.values(f.from)
		if err != nil {
			return err
		}
		if e.decide(o, []string{"tags", f.name}, f.line, want, tags[f.name], from) {
			set = append(set, tag{f.name, want})
		}
	}
	if len(set) == 0 {
		return nil
	}
	b, _ := json.Marshal(set)
	if _, err := e.awsRun(ctx, t.region, "ecs", "tag-resource", "--resource-arn", t.arn, "--tags", string(b)); err != nil {
		return err
	}
	for _, s := range set {
		tags[s.Key] = s.Value
	}
	return nil
}

// execAWS runs the AWS CLI.
func execAWS(ctx context.Context, region string, args ...string) ([]byte, error) {
	bin := os.Getenv("DTT_AWS_CLI")
	if bin == "" {
		bin = "aws"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("the aws provider runs the AWS CLI (%q), which isn't installed here -- install it or set DTT_AWS_CLI", bin)
	}
	full := append(append([]string{}, args...), "--output", "json")
	if region != "" {
		full = append(full, "--region", region)
	}
	cmd := exec.CommandContext(ctx, path, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("aws %s %s: %v: %s", args[0], args[1], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
