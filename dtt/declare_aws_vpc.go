package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// This file is the aws provider's VPCs (see declare_aws.go), which an assert
// can create and delete, with their subnets nested under them.
//
//	aws.accounts.current.regions.us-east-1.ec2.vpcs:
//	  select:
//	    tags:
//	      Name: ufa-sandbox             (* for any value)
//	  assert:
//	    exists: true                    (the default; false deletes it)
//	    cidr_block: 10.42.0.0/16        (fixed once it exists)
//	    enable_dns_hostnames: "true"
//	    enable_dns_support: "true"
//	    tags:
//	      managed-by: dtt
//	    subnets:                        (one, or a list)
//	      - select:
//	          cidr_block: 10.42.1.0/24
//	        state:
//	          exists: true              (the default; false deletes it)
//	          availability_zone: us-east-1a   (fixed once it exists)
//	          map_public_ip_on_launch: "true"
//	          tags:
//	            Name: ufa-sandbox-a
//
// Every VPC whose tags match is one object, <path>[<vpc id>], and the
// assert applies to each of them: a select that matches several VPCs isn't
// an error, but the assert then has to suit them all (asserting one
// cidr_block on two VPCs that differ fails). When nothing matches and the
// select's tags are all plain values (no *), the select names a VPC that
// doesn't exist yet: one object, <path>[<tags>], with exists false, which an
// assert creates -- tagged with the select's tags, so the next run finds it
// -- and then knows by its id.
//
// cidr_block and a subnet's availability_zone can't change once made: when
// one holds something else, an assert fails (no from value, image or force
// changes it) and a read reports it held. exists: false deletes, by the
// from rule (exists: true isn't empty, so it needs a from value, the image
// or force). Subnets asserted gone are deleted before their VPC; AWS refuses
// to delete a VPC while anything (another subnet, a gateway) is still in it,
// and the run fails with what AWS said.
//
// Delete mode asserts exists: false on a VPC whose select names it (plain
// tag values, so an assert would make it) and on each of its subnets in the
// document, by the same from rule; VPCs found by a pattern are left alone.

// awsVPC is the provider's handle on a VPC: its id, or (not made yet) the
// select's tags to make it with. named: the select's tags are plain values,
// so an assert would make the VPC if it weren't there.
type awsVPC struct {
	id, region string
	selTags    map[string]string
	named      bool
}

var (
	vpcFields    = []string{"exists", "cidr_block", "enable_dns_hostnames", "enable_dns_support", "tags", "subnets"}
	subnetFields = []string{"exists", "availability_zone", "map_public_ip_on_launch", "tags"}
	// awsBoolFields are read as true or false.
	awsBoolFields = []string{"exists", "enable_dns_hostnames", "enable_dns_support", "map_public_ip_on_launch"}
)

type vpcSpec struct {
	fields  []fieldSpec // exists, cidr_block, enable_dns_*
	tags    []fieldSpec
	subnets []subnetSpec
	line    int
}

type subnetSpec struct {
	cidr   *yNode
	fields []fieldSpec // exists, availability_zone, map_public_ip_on_launch
	tags   []fieldSpec
	line   int
}

func checkVPCSelect(st *statement) error {
	if st.sel == nil {
		return nil
	}
	if st.sel.kind != yMap {
		return fmt.Errorf("select VPCs by tags (select: {tags: {Name: ...}})")
	}
	if err := onlyKeys(st.sel, "VPCs' select", "tags"); err != nil {
		return err
	}
	tags := st.sel.get("tags")
	if tags == nil {
		return nil
	}
	if tags.kind != yMap {
		return fmt.Errorf("line %d: select.tags should be a mapping of key: value", tags.line)
	}
	for i, v := range tags.vals {
		if v.kind != yScalar {
			return fmt.Errorf("line %d: tag %s should be a single value", v.line, tags.keys[i])
		}
	}
	return nil
}

// splitAWSFields splits a VPC's or subnet's mapping into its single-valued
// fields and its tags (a mapping of key: value, each with a from beside it).
func splitAWSFields(n *yNode, what string, allowed []string) (fields, tags []fieldSpec, rest map[string]*yNode, err error) {
	all, err := assertFields(n, what, allowed...)
	if err != nil {
		return nil, nil, nil, err
	}
	rest = map[string]*yNode{}
	for _, f := range all {
		switch f.name {
		case "tags", "subnets":
			if len(f.from) > 0 {
				return nil, nil, nil, fmt.Errorf("line %d: %s takes its from values inside it (from.<key>), not from.%s", f.line, f.name, f.name)
			}
			rest[f.name] = f.want
		default:
			if f.want.kind != yScalar {
				return nil, nil, nil, fmt.Errorf("line %d: %s's %s should be a single value", f.line, what, f.name)
			}
			fields = append(fields, f)
		}
	}
	if t := rest["tags"]; t != nil {
		if tags, err = assertFields(t, "tags"); err != nil {
			return nil, nil, nil, err
		}
		for _, f := range tags {
			if f.want.kind != yScalar {
				return nil, nil, nil, fmt.Errorf("line %d: tag %s should be a single value", f.line, f.name)
			}
		}
	}
	return fields, tags, rest, nil
}

func parseVPCAssert(body *yNode) (vpcSpec, error) {
	spec := vpcSpec{line: body.line}
	if body.kind != yMap {
		return spec, fmt.Errorf("line %d: a VPC's assert is a mapping of its fields (%s)", body.line, strings.Join(vpcFields, ", "))
	}
	var rest map[string]*yNode
	var err error
	if spec.fields, spec.tags, rest, err = splitAWSFields(body, "a VPC", vpcFields); err != nil {
		return spec, err
	}
	subnets := rest["subnets"]
	if subnets == nil || isEmpty(subnets) {
		return spec, nil
	}
	items := []*yNode{subnets}
	if subnets.kind == ySeq {
		items = subnets.items
	}
	for _, n := range items {
		if n.kind != yMap {
			return spec, fmt.Errorf("line %d: each of subnets is a mapping of select and state", n.line)
		}
		if err := onlyKeys(n, "a subnet", "select", "state"); err != nil {
			return spec, err
		}
		sel := n.get("select")
		if sel == nil || sel.kind != yMap {
			return spec, fmt.Errorf("line %d: a subnet needs a select mapping (cidr_block)", n.line)
		}
		if err := onlyKeys(sel, "a subnet's select", "cidr_block"); err != nil {
			return spec, err
		}
		s := subnetSpec{cidr: sel.get("cidr_block"), line: n.line}
		if s.cidr == nil || isEmpty(s.cidr) || s.cidr.kind != yScalar {
			return spec, fmt.Errorf("line %d: a subnet's select needs cidr_block (unique within its VPC)", sel.line)
		}
		if st := n.get("state"); st != nil && !isEmpty(st) {
			if s.fields, s.tags, _, err = splitAWSFields(st, "a subnet", subnetFields); err != nil {
				return spec, err
			}
		}
		spec.subnets = append(spec.subnets, s)
	}
	return spec, nil
}

// ---- Measuring ----

type ec2Tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type ec2VPC struct {
	VpcId     string
	CidrBlock string
	State     string
	IsDefault bool
	Tags      []ec2Tag
}

type ec2Subnet struct {
	SubnetId            string
	VpcId               string
	CidrBlock           string
	AvailabilityZone    string
	MapPublicIpOnLaunch bool
	State               string
	Tags                []ec2Tag
}

func ec2TagMap(tags []ec2Tag) map[string]any {
	m := map[string]any{}
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

// ec2TagList is tags as EC2 takes them, in key order.
func ec2TagList(tags map[string]string) []ec2Tag {
	out := []ec2Tag{}
	for _, k := range sortedKeys(tags) {
		out = append(out, ec2Tag{k, tags[k]})
	}
	return out
}

// ec2 makes one boto3 ec2 call (describe_vpcs), reading its response into
// out.
func (e *engine) ec2(ctx context.Context, region string, out any, op string, params map[string]any) error {
	b, err := e.awsRun(ctx, region, "ec2", op, params)
	if err != nil {
		return err
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("aws ec2 %s: %v", op, err)
	}
	return nil
}

func (e *engine) describeVPCs(ctx context.Context, region string, ids ...string) ([]ec2VPC, error) {
	params := map[string]any{}
	if len(ids) > 0 {
		params["VpcIds"] = ids
	}
	var out struct{ Vpcs []ec2VPC }
	err := e.ec2(ctx, region, &out, "describe_vpcs", params)
	return out.Vpcs, err
}

func (e *engine) describeSubnets(ctx context.Context, region, vpcID string) ([]ec2Subnet, error) {
	var out struct{ Subnets []ec2Subnet }
	err := e.ec2(ctx, region, &out, "describe_subnets", map[string]any{"Filters": []any{map[string]any{"Name": "vpc-id", "Values": []string{vpcID}}}})
	sort.Slice(out.Subnets, func(i, j int) bool { return out.Subnets[i].CidrBlock < out.Subnets[j].CidrBlock })
	return out.Subnets, err
}

// vpcAttribute reads enableDnsHostnames or enableDnsSupport.
func (e *engine) vpcAttribute(ctx context.Context, region, id, attr string) (bool, error) {
	var out map[string]json.RawMessage
	if err := e.ec2(ctx, region, &out, "describe_vpc_attribute", map[string]any{"VpcId": id, "Attribute": attr}); err != nil {
		return false, err
	}
	var v struct{ Value bool }
	json.Unmarshal(out[strings.ToUpper(attr[:1])+attr[1:]], &v)
	return v.Value, nil
}

func subnetState(s *ec2Subnet, cidr string) map[string]any {
	if s == nil {
		return map[string]any{"exists": false, "cidr_block": cidr}
	}
	return map[string]any{"exists": true, "id": s.SubnetId, "cidr_block": s.CidrBlock, "availability_zone": s.AvailabilityZone,
		"map_public_ip_on_launch": s.MapPublicIpOnLaunch, "state": s.State, "tags": ec2TagMap(s.Tags)}
}

// vpcState measures VPC v whole: its attributes and its subnets.
func (e *engine) vpcState(ctx context.Context, region string, v ec2VPC) (map[string]any, error) {
	hostnames, err := e.vpcAttribute(ctx, region, v.VpcId, "enableDnsHostnames")
	if err != nil {
		return nil, err
	}
	support, err := e.vpcAttribute(ctx, region, v.VpcId, "enableDnsSupport")
	if err != nil {
		return nil, err
	}
	subnets, err := e.describeSubnets(ctx, region, v.VpcId)
	if err != nil {
		return nil, err
	}
	list := []any{}
	for i := range subnets {
		list = append(list, subnetState(&subnets[i], ""))
	}
	return map[string]any{"exists": true, "id": v.VpcId, "cidr_block": v.CidrBlock, "state": v.State, "is_default": v.IsDefault,
		"enable_dns_hostnames": hostnames, "enable_dns_support": support, "tags": ec2TagMap(v.Tags), "subnets": list}, nil
}

func newVPCState(tags map[string]string) map[string]any {
	m := map[string]any{}
	for k, v := range tags {
		m[k] = v
	}
	return map[string]any{"exists": false, "tags": m}
}

// tagsKey names a VPC not made yet by its select's tags: Name=ufa-sandbox.
func tagsKey(tags map[string]string) string {
	var parts []string
	for _, k := range sortedKeys(tags) {
		parts = append(parts, k+"="+tags[k])
	}
	return strings.Join(parts, ",")
}

func resolveVPCs(ctx context.Context, e *engine, st *statement, region string) ([]*dobj, error) {
	want := map[string]string{}
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
	}
	vpcs, err := e.describeVPCs(ctx, region)
	if err != nil {
		return nil, err
	}
	named := len(want) > 0
	for _, v := range want {
		if v == "*" {
			named = false
		}
	}
	base := st.pathString()
	var objs []*dobj
	for _, v := range vpcs {
		if !tagsMatch(ec2TagMap(v.Tags), want) {
			continue
		}
		state, err := e.vpcState(ctx, region, v)
		if err != nil {
			return nil, err
		}
		objs = append(objs, e.addObject(base+"["+v.VpcId+"]", base, st.line, state, &awsVPC{id: v.VpcId, region: region, named: named}))
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "aws: %d VPC(s) match %s's select\n", len(objs), base)
	}
	if len(objs) > 0 || !named {
		return objs, nil // a pattern (or no select) names no one VPC to make
	}
	// The select names one VPC, not made yet.
	id := base + "[" + tagsKey(want) + "]"
	return []*dobj{e.addObject(id, base, st.line, newVPCState(want), &awsVPC{region: region, selTags: want, named: true})}, nil
}

// ---- Asserting ----

// awsField is an asserted field, evaluated.
type awsField struct {
	name, want string
	from       []any
	line       int
}

func (e *engine) awsFields(fs []fieldSpec, what string) (map[string]awsField, []awsField, error) {
	byName := map[string]awsField{}
	var ordered []awsField
	for _, f := range fs {
		want, err := e.text(f.want, what+f.name)
		if err != nil {
			return nil, nil, err
		}
		if contains(awsBoolFields, f.name) {
			b, err := parseBool(want)
			if err != nil {
				return nil, nil, fmt.Errorf("line %d: %s: %v", f.line, f.name, err)
			}
			want = fmt.Sprint(b)
		}
		from, err := e.values(f.from)
		if err != nil {
			return nil, nil, err
		}
		af := awsField{f.name, want, from, f.line}
		byName[f.name] = af
		ordered = append(ordered, af)
	}
	return byName, ordered, nil
}

// fixed is the rule for a field that can't change once its object exists:
// unchanged when it holds want; otherwise held in a read, and a failed
// assert -- no from value, image or force can change it.
func (e *engine) fixed(o *dobj, field string, line int, want, have any) error {
	f := FieldOutcome{Object: o.img.ID, Field: field, Was: have, Want: want, Line: line}
	if valueKey(want) == valueKey(have) {
		f.Outcome, f.Why = "unchanged", "it already holds that"
		e.record(f)
		return nil
	}
	f.Outcome, f.Why = "held", field+" can't change once it exists, whatever the from rule says; an assert fails"
	e.record(f)
	if e.changes() {
		return fmt.Errorf("line %d: %s's %s is %v and can't change to %v once it exists", line, o.img.ID, field, have, want)
	}
	return nil
}

// decideTags applies the from rule to each asserted tag, returning those to
// set.
func (e *engine) decideTags(o *dobj, tags []awsField) map[string]string {
	have, _ := o.img.State["tags"].(map[string]any)
	set := map[string]string{}
	for _, t := range tags {
		if e.decide(o, []string{"tags", t.name}, t.line, t.want, have[t.name], t.from) {
			set[t.name] = t.want
		}
	}
	return set
}

func (e *engine) createTags(ctx context.Context, region, id string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	return e.ec2(ctx, region, nil, "create_tags", map[string]any{"Resources": []string{id}, "Tags": ec2TagList(tags)})
}

func tagSpec(resource string, tags map[string]string) []any {
	return []any{map[string]any{"ResourceType": resource, "Tags": ec2TagList(tags)}}
}

// subnetPlan is one asserted subnet: measured, evaluated and checked before
// its VPC changes anything.
type subnetPlan struct {
	spec   subnetSpec
	cidr   string
	o      *dobj
	exists bool
	fields map[string]awsField
	tags   []awsField
	gone   bool // delete mode: asserted gone whatever its fields say
}

func (e *engine) planSubnet(ctx context.Context, v *awsVPC, vpcObj *dobj, measured []ec2Subnet, s subnetSpec) (*subnetPlan, error) {
	cidr, err := e.text(s.cidr, "select.cidr_block")
	if err != nil {
		return nil, err
	}
	var found *ec2Subnet
	for i := range measured {
		if measured[i].CidrBlock == cidr {
			found = &measured[i]
		}
	}
	p := &subnetPlan{spec: s, cidr: cidr, exists: found != nil, gone: e.deleting()}
	p.o = e.addObject(vpcObj.img.ID+".subnets["+cidr+"]", vpcObj.img.Path+".subnets", s.line, subnetState(found, cidr), nil)
	if p.fields, _, err = e.awsFields(s.fields, ""); err != nil {
		return nil, err
	}
	if _, p.tags, err = e.awsFields(s.tags, "tags."); err != nil {
		return nil, err
	}
	if az, ok := p.fields["availability_zone"]; ok && p.exists && p.wantExists() {
		if err := e.fixed(p.o, "availability_zone", az.line, az.want, found.AvailabilityZone); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *subnetPlan) wantExists() bool {
	if p.gone {
		return false
	}
	f, ok := p.fields["exists"]
	return !ok || f.want == "true"
}

// apply asserts the subnet in VPC v (whose id is "" in a read of a VPC not
// made yet).
func (p *subnetPlan) apply(ctx context.Context, e *engine, v *awsVPC) error {
	o := p.o
	ex, explicit := p.fields["exists"]
	if !explicit {
		ex = awsField{name: "exists", want: "true", line: p.spec.line}
	}
	if !p.wantExists() {
		if p.exists && e.decide(o, []string{"exists"}, ex.line, false, true, ex.from) {
			if err := e.ec2(ctx, v.region, nil, "delete_subnet", map[string]any{"SubnetId": fmt.Sprint(o.img.State["id"])}); err != nil {
				return err
			}
			o.img.State = subnetState(nil, p.cidr)
		} else if !p.exists {
			e.decide(o, []string{"exists"}, ex.line, false, false, ex.from)
		}
		return nil
	}
	changeExists := false
	if explicit || !p.exists {
		changeExists = e.decide(o, []string{"exists"}, ex.line, true, p.exists, ex.from)
	}
	az, hasAZ := p.fields["availability_zone"]
	if hasAZ && !p.exists {
		e.decide(o, []string{"availability_zone"}, az.line, az.want, nil, az.from)
	}
	setPublic := false
	pub, hasPub := p.fields["map_public_ip_on_launch"]
	if hasPub {
		setPublic = e.decide(o, []string{"map_public_ip_on_launch"}, pub.line, pub.want, o.img.State["map_public_ip_on_launch"], pub.from)
	}
	tags := e.decideTags(o, p.tags)

	id, _ := o.img.State["id"].(string)
	if !p.exists {
		if !changeExists {
			return nil
		}
		params := map[string]any{"VpcId": v.id, "CidrBlock": p.cidr}
		if hasAZ {
			params["AvailabilityZone"] = az.want
		}
		if len(tags) > 0 {
			params["TagSpecifications"] = tagSpec("subnet", tags)
		}
		var made struct{ Subnet ec2Subnet }
		if err := e.ec2(ctx, v.region, &made, "create_subnet", params); err != nil {
			return err
		}
		id, tags = made.Subnet.SubnetId, nil
	} else if !setPublic && len(tags) == 0 {
		return nil
	}
	if setPublic {
		on := map[string]any{"Value": pub.want == "true"}
		if err := e.ec2(ctx, v.region, nil, "modify_subnet_attribute", map[string]any{"SubnetId": id, "MapPublicIpOnLaunch": on}); err != nil {
			return err
		}
	}
	if err := e.createTags(ctx, v.region, id, tags); err != nil {
		return err
	}
	// Measure it again, as the changes left it.
	subnets, err := e.describeSubnets(ctx, v.region, v.id)
	if err != nil {
		return err
	}
	for i := range subnets {
		if subnets[i].SubnetId == id {
			o.img.State = subnetState(&subnets[i], p.cidr)
		}
	}
	return nil
}

func (v *awsVPC) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	spec, err := parseVPCAssert(body)
	if err != nil {
		return err
	}
	fields, _, err := e.awsFields(spec.fields, "")
	if err != nil {
		return err
	}
	_, tags, err := e.awsFields(spec.tags, "tags.")
	if err != nil {
		return err
	}
	exists := o.img.State["exists"] == true
	ex, explicit := fields["exists"]
	if !explicit {
		ex = awsField{name: "exists", want: "true", line: spec.line}
	}
	wantExists := ex.want == "true"
	if e.deleting() {
		if !v.named {
			e.keep(o, spec.line, "found by a tag pattern, which an assert never creates; delete leaves it")
			return nil
		}
		wantExists = false // the VPC and its subnets go, whatever else is asserted
	}
	cidr, hasCIDR := fields["cidr_block"]

	// Everything is measured and checked before anything changes: the fixed
	// fields, here and in each subnet.
	if exists && wantExists && hasCIDR {
		if err := e.fixed(o, "cidr_block", cidr.line, cidr.want, o.img.State["cidr_block"]); err != nil {
			return err
		}
	}
	var measured []ec2Subnet
	if exists {
		if measured, err = e.describeSubnets(ctx, v.region, v.id); err != nil {
			return err
		}
	}
	var plans []*subnetPlan
	for _, s := range spec.subnets {
		p, err := e.planSubnet(ctx, v, o, measured, s)
		if err != nil {
			return err
		}
		if !wantExists && p.wantExists() {
			return fmt.Errorf("line %d: the VPC is asserted gone, so its subnets can only be asserted gone too (exists: false)", s.line)
		}
		plans = append(plans, p)
	}

	if !wantExists {
		for _, p := range plans {
			if err := p.apply(ctx, e, v); err != nil {
				return err
			}
		}
		if exists && e.decide(o, []string{"exists"}, ex.line, false, true, ex.from) {
			if err := e.ec2(ctx, v.region, nil, "delete_vpc", map[string]any{"VpcId": v.id}); err != nil {
				return err
			}
			o.img.State = map[string]any{"exists": false, "id": v.id, "tags": o.img.State["tags"]}
		} else if !exists {
			e.decide(o, []string{"exists"}, ex.line, false, false, ex.from)
		}
		return nil
	}

	changeExists := false
	if explicit || !exists {
		changeExists = e.decide(o, []string{"exists"}, ex.line, true, exists, ex.from)
	}
	if !exists {
		if v.selTags == nil {
			return fmt.Errorf("line %d: %s doesn't exist and has no select to make it by", spec.line, o.img.ID)
		}
		if !hasCIDR {
			return fmt.Errorf("line %d: %s doesn't exist yet; making it needs cidr_block", spec.line, o.img.ID)
		}
		e.decide(o, []string{"cidr_block"}, cidr.line, cidr.want, nil, cidr.from)
	}
	var attrs []awsField
	for _, name := range []string{"enable_dns_hostnames", "enable_dns_support"} {
		if f, ok := fields[name]; ok && e.decide(o, []string{name}, f.line, f.want, o.img.State[name], f.from) {
			attrs = append(attrs, f)
		}
	}
	setTags := e.decideTags(o, tags)

	if !exists {
		if !changeExists || !e.changes() {
			// A read: what the subnets would do in the VPC not made yet.
			for _, p := range plans {
				if err := p.apply(ctx, e, v); err != nil {
					return err
				}
			}
			return nil
		}
		all := map[string]string{}
		for k, val := range v.selTags {
			all[k] = val
		}
		for k, val := range setTags {
			all[k] = val
		}
		var made struct{ Vpc ec2VPC }
		if err := e.ec2(ctx, v.region, &made, "create_vpc", map[string]any{"CidrBlock": cidr.want, "TagSpecifications": tagSpec("vpc", all)}); err != nil {
			return err
		}
		v.id, v.selTags, setTags = made.Vpc.VpcId, nil, nil
		e.renameObject(o, o.img.Path+"["+v.id+"]")
		for _, p := range plans {
			e.renameObject(p.o, o.img.ID+".subnets["+p.cidr+"]")
		}
	}
	for _, f := range attrs {
		// One attribute per call.
		attr := map[string]string{"enable_dns_hostnames": "EnableDnsHostnames", "enable_dns_support": "EnableDnsSupport"}[f.name]
		if err := e.ec2(ctx, v.region, nil, "modify_vpc_attribute", map[string]any{"VpcId": v.id, attr: map[string]any{"Value": f.want == "true"}}); err != nil {
			return err
		}
	}
	if err := e.createTags(ctx, v.region, v.id, setTags); err != nil {
		return err
	}
	for _, p := range plans {
		if err := p.apply(ctx, e, v); err != nil {
			return err
		}
	}
	// Measure it again, as the changes left it.
	if e.changes() && (!exists || len(attrs) > 0 || len(setTags) > 0 || len(plans) > 0) {
		vpcs, err := e.describeVPCs(ctx, v.region, v.id)
		if err != nil {
			return err
		}
		if len(vpcs) == 1 {
			if o.img.State, err = e.vpcState(ctx, v.region, vpcs[0]); err != nil {
				return err
			}
		}
	}
	return nil
}

// renameObject gives o a new id: a VPC made this run is known by its id
// from then on, so a later run's image matches it.
func (e *engine) renameObject(o *dobj, id string) {
	delete(e.byID, o.img.ID)
	o.img.ID = id
	e.byID[id] = o
}
