# DTT: Additional Proposed Examples

**Status: proposals for discussion. Unless marked as moved, none of this
is implemented or committed to.** The implemented examples are in
[`../examples/compositions/`](../examples/compositions/). The README's
[Declarative tools](../README.md#declarative-tools-read-assert-and-delete) section
covers what works today.

Each proposal below is a document as we might write it, followed by what it
would need beyond the github, filesystem and aws providers as they were
when it was proposed. They
all keep the current rules. Reading returns an image and changes nothing.
Asserting changes a field only from empty, from its `from.<field>` values,
from what a supplied image saw, or (force) from anything, and it always
returns an image.

The numbers stay as first given, so earlier discussion still lines up.

## GitHub

### 1. Label every open condoc PR, and request a reviewer — moved

**Implemented**, as written, in
[`../examples/compositions/github-condoc-labels.dtt.yaml`](../examples/compositions/github-condoc-labels.dtt.yaml).
The open questions were answered:

- A pattern in `select` that matches nothing counts as success.
- A from rule can apply to a list or to an element of the list. `labels`
  and `reviewers` are asserted member by member (`labels[needs-review]` in
  the image). A from value naming a member lets that member be removed; a
  from value that is a list lets any member change while the whole list is
  as it says. See the README's from rule.

### 2. Keep one tracking issue per condoc, closed when its PR merges

The proposal adds issues as a second kind of child object, and lets a field
take its value from another object measured in the same document.

```yaml
github.users.user-1.repos.AI-sandboxing:
  read: *
  alias: the-repo

the-repo:
  assert:
    issues:
      select:
        title: "Track condoc blah1"
      state:
        exists: true
        body: |
          Tracks ${{the-repo.url}}/pull/${{the-repo.prs.condoc/blah1/main.number}}.
        state: open
        from.state:
          new: ''                   # only when the issue is first created
```

Open questions:
- How should a reference address a child object? `prs.<from>` is
  ambiguous when `from` contains dots or slashes. One option is
  `prs[condoc/blah1/main->main]`, which matches the image's object ids.
- Is matching an issue by its title stable enough, or does matching need a
  label or marker comment?

### 7. Create a new repository with the date in its name — moved

**Implemented** in
[`../examples/compositions/github-repo-dated.dtt.yaml`](../examples/compositions/github-repo-dated.dtt.yaml),
as written except for its path: user-1 is a GitHub user, not an
organization, so the example uses `github.users.user-1.repos`
(`github.orgs.<owner>.repos` remains for organizations).
The open questions were answered:

- Creation lives on the owner's set of repositories
  (`github.users|orgs.<owner>.repos`, the name in `select`), and `exists: true`
  is implicit: it is the default, so it may be left out.
- `auto_init` has meaning only at create time. It is applied when the
  repository is made and measured `false` afterwards, so it isn't asserted
  on a repository that exists. (Later, a "transient" kind of field may
  describe this.)
- A new name each day, never selecting yesterday's again, is the intent.
- `exists: false` is supported, but rejected unless an approval field
  stands beside it: `approve_delete: <owner>/<name>`, naming the
  repository. The from rule then applies as usual.

## Files

### 3. Text files from a template, and removing what was retired — moved

**Implemented** in
[`../examples/compositions/filesystem-text-template.dtt.yaml`](../examples/compositions/filesystem-text-template.dtt.yaml),
with `mode: "0644"` added to `motd.txt`. `mode`, `owner` and `group` are
now optional item fields, each with its own from rule (the README covers
them).

One question stays open, restated:
- **A statement-level force.** Today force applies to the whole run: the
  `--force` flag, or the built-in tools' `force` input, makes every
  asserted field in the document change whatever it holds. Inside a
  document, `from.<field>: "*"` already does that for one field. Should a
  single statement be able to opt in for all of its own fields — say
  `force: true` beside `assert:` — so one statement overwrites everything
  it asserts while the rest of the document keeps the from rule? Or is
  per-field `from.<field>: "*"` enough, keeping force a decision the caller
  makes rather than the document?

### 4. Assert part of a JSON file and leave the rest alone

Today `content` replaces the whole file. This proposal adds a `keys`
form: the from rule applies to each key path, and keys the document
doesn't mention are kept.

```yaml
filesystem.items:
  select:
    abspath: /home/agent/.config/tool/
  alias: tool-config

tool-config:
  assert:
    items:
      settings.json:
        type: json
        keys:                       # instead of content
          editor.theme: dark
          editor.fontSize: "14"
          from.editor.theme:
            default: light          # change it only from the default
```

Open questions:
- Key paths with dots in their names need quoting rules.
- Should numbers and booleans keep their JSON types? Today every scalar is
  a string, so `"14"` stays a string. A typed scalar form, or a `json:`
  inline value, would fix that.

## AWS

### 5. S3 bucket tags and versioning

The proposal adds a second AWS object kind: buckets selected by name
prefix, with a nested non-tag field.

```yaml
aws.accounts.current.s3.buckets:
  select:
    name: ufa-artifacts-*
  assert:
    tags:
      owner: ufa
      from.owner: [unknown, '']
    versioning: Enabled
    from.versioning: Suspended      # never "un-suspend" from anything else
```

Open questions:
- S3 is global, but bucket calls are regional. Should `regions.<r>` in the
  path be required or optional?
- Some fields can't be cleared once set: versioning can be suspended but
  never removed. Should the provider refuse to assert those values, or
  only report them as held?

### 6. Scale a service down out of hours, only from what we last set

ECS services as objects, with a numeric field. The from rule keeps a
person's manual scale-up from being undone. It works best run with the
previous run's image.

```yaml
timestamp:
  select: now

aws.accounts.current.regions.us-east-1.ecs.services:
  select:
    cluster: dev
    tags:
      schedule: office-hours
  assert:
    desired_count: "0"
    from.desired_count: ["1"]       # only from the normal daytime size
```

Run nightly as `dtt run aws --arg mode=assert --arg from_image=last-night.json`,
keeping each run's `image` output for the next run.

Open questions:
- Should the document be able to say *when* it applies? For example, a
  `when:` built from `${{timestamp...}}` fields. Or is scheduling only the
  caller's job, through a control step on a timer?
- Should numeric fields compare as numbers rather than as strings?

### 8. Create a new VPC — moved

**Implemented** in
[`../examples/compositions/aws-vpc-sandbox.dtt.yaml`](../examples/compositions/aws-vpc-sandbox.dtt.yaml),
with one subnet nested under the VPC. The open questions were answered:

- A select matching several VPCs isn't invalid: the assert applies to each
  of them, and the rest of the document decides whether that passes or
  fails (asserting one `cidr_block` on two VPCs that differ fails, say).
  A VPC is created only when nothing matches and the select's tags name
  one (no `*`).
- Changing `cidr_block` fails the run, before anything changes; no from
  value or force gets past it. A read reports it `held`. A subnet's
  `availability_zone` works the same way. (Later these may be "transient".)
- Subnets are child objects of the VPC (`subnets:` under it, each selected
  by its `cidr_block`), because they belong to that VPC. Route tables and
  internet gateways aren't covered yet.
- `exists: false` deletes, by the from rule. Subnets asserted gone are
  deleted before their VPC; AWS refuses to delete a VPC that still has
  anything in it, and the run fails with its message.
