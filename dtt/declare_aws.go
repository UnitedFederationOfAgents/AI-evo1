package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// This file is the aws provider (see declare.go): ECS tasks and their tags,
// through boto3 (aws_boto3.py, run with python), so credentials and region
// come from wherever boto3 finds them.
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
// (tags.<key>). $DTT_AWS_PYTHON names the python to run boto3 with (see
// findBoto3). VPCs (...ec2.vpcs), which an assert can create and delete, are
// in declare_aws_vpc.go.
type awsProvider struct{}

// awsRunner makes one boto3 call -- service's operation (describe_vpcs),
// with params as boto3 takes them -- returning its response as JSON.
type awsRunner func(ctx context.Context, region, service, op string, params map[string]any) ([]byte, error)

var awsAccountRe = regexp.MustCompile(`^[0-9]{12}$`)

type awsTask struct {
	arn, cluster, region string
}

// The kinds of AWS object a path can end in.
const (
	awsECSTasks = "ecs.tasks"
	awsEC2VPCs  = "ec2.vpcs"
)

// awsPath reads aws.accounts.<current|id>[.regions.<region>].<kind>.
func awsPath(path []string) (account, region, kind string, err error) {
	bad := fmt.Errorf("aws paths are aws.accounts.<current|id>[.regions.<region>].ecs.tasks or ...ec2.vpcs")
	if len(path) < 5 || path[1] != "accounts" {
		return "", "", "", bad
	}
	account, rest := path[2], path[3:]
	if rest[0] == "regions" {
		if len(rest) < 4 {
			return "", "", "", bad
		}
		region, rest = rest[1], rest[2:]
	}
	if kind = strings.Join(rest, "."); kind != awsECSTasks && kind != awsEC2VPCs {
		return "", "", "", bad
	}
	if account != "current" && !awsAccountRe.MatchString(account) {
		return "", "", "", fmt.Errorf("account %q should be current or a 12-digit account id", account)
	}
	return account, region, kind, nil
}

// checkAWSAccount checks an account id in the path against the credentials.
func (e *engine) checkAWSAccount(ctx context.Context, account, region string) error {
	if account == "current" {
		return nil
	}
	out, err := e.awsRun(ctx, region, "sts", "get_caller_identity", nil)
	if err != nil {
		return err
	}
	var id struct{ Account string }
	if err := json.Unmarshal(out, &id); err != nil {
		return fmt.Errorf("aws sts get_caller_identity: %v", err)
	}
	if id.Account != account {
		return fmt.Errorf("the AWS credentials here are for account %s, not %s", id.Account, account)
	}
	return nil
}

func (awsProvider) checkSelect(st *statement) error {
	_, _, kind, err := awsPath(st.path)
	if err != nil {
		return err
	}
	if kind == awsEC2VPCs {
		return checkVPCSelect(st)
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

func (awsProvider) checkAssert(path []string, body *yNode) error {
	if _, _, kind, _ := awsPath(path); kind == awsEC2VPCs {
		_, err := parseVPCAssert(body)
		return err
	}
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
	account, region, kind, _ := awsPath(st.path)
	if err := e.checkAWSAccount(ctx, account, region); err != nil {
		return nil, err
	}
	if kind == awsEC2VPCs {
		return resolveVPCs(ctx, e, st, region)
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
		out, err := e.awsRun(ctx, region, "ecs", "list_clusters", nil)
		if err != nil {
			return nil, err
		}
		var list struct{ ClusterArns []string }
		if err := json.Unmarshal(out, &list); err != nil {
			return nil, fmt.Errorf("aws ecs list_clusters: %v", err)
		}
		clusters = list.ClusterArns
	}

	base := st.pathString()
	var objs []*dobj
	for _, cluster := range clusters {
		out, err := e.awsRun(ctx, region, "ecs", "list_tasks", map[string]any{"cluster": cluster})
		if err != nil {
			return nil, err
		}
		var list struct{ TaskArns []string }
		if err := json.Unmarshal(out, &list); err != nil {
			return nil, fmt.Errorf("aws ecs list_tasks: %v", err)
		}
		for start := 0; start < len(list.TaskArns); start += 100 {
			batch := list.TaskArns[start:min(start+100, len(list.TaskArns))]
			out, err := e.awsRun(ctx, region, "ecs", "describe_tasks", map[string]any{"cluster": cluster, "include": []string{"TAGS"}, "tasks": batch})
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
				return nil, fmt.Errorf("aws ecs describe_tasks: %v", err)
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
	if v, ok := o.handle.(*awsVPC); ok {
		return v.assert(ctx, e, o, body)
	}
	fields, err := parseAWSAssert(body)
	if err != nil {
		return err
	}
	if e.deleting() {
		e.keep(o, body.line, "an ECS task's tags are only changed, never made; delete leaves them")
		return nil
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
	if _, err := e.awsRun(ctx, t.region, "ecs", "tag_resource", map[string]any{"resourceArn": t.arn, "tags": set}); err != nil {
		return err
	}
	for _, s := range set {
		tags[s.Key] = s.Value
	}
	return nil
}

//go:embed aws_boto3.py
var awsBoto3Script string

var (
	boto3Once sync.Once
	boto3Argv []string // the python that runs awsBoto3Script, and its arguments
	boto3Err  error
)

// findBoto3 picks the python to run boto3 with: $DTT_AWS_PYTHON; else
// python3, if it can import boto3; else uv, which fetches boto3 itself.
func findBoto3(ctx context.Context) ([]string, error) {
	boto3Once.Do(func() {
		if py := os.Getenv("DTT_AWS_PYTHON"); py != "" {
			boto3Argv = []string{py, "-c", awsBoto3Script}
			return
		}
		if py, err := exec.LookPath("python3"); err == nil && exec.CommandContext(ctx, py, "-c", "import boto3").Run() == nil {
			boto3Argv = []string{py, "-c", awsBoto3Script}
			return
		}
		if uv, err := exec.LookPath("uv"); err == nil {
			boto3Argv = []string{uv, "run", "--quiet", "--no-project", "--with", "boto3", "python", "-c", awsBoto3Script}
			return
		}
		boto3Err = fmt.Errorf("the aws provider uses boto3, and found no python here that has it -- pip install boto3 (for python3), install uv, or set DTT_AWS_PYTHON to a python that has boto3")
	})
	return boto3Argv, boto3Err
}

// execAWS makes one boto3 call: service's operation, with params as boto3
// takes them.
func execAWS(ctx context.Context, region, service, op string, params map[string]any) ([]byte, error) {
	argv, err := findBoto3(ctx)
	if err != nil {
		return nil, err
	}
	req, err := json.Marshal(map[string]any{"region": region, "service": service, "operation": op, "params": params})
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(req)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("aws %s %s: %s", service, op, msg)
	}
	return out, nil
}
