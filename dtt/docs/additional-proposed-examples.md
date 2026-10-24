# DTT: Additional Proposed Examples

**Status: proposals for discussion. None of this is implemented or
committed to.** The implemented examples are in [`../examples/`](../examples/).
The README's [Declarative tools](../README.md#declarative-tools-read-and-assert)
section covers what works today.

Each proposal below is a document as we might write it, followed by what it
would need beyond the current github, filesystem and aws providers. They
all keep the current rules. Reading returns an image and changes nothing.
Asserting changes a field only from empty, from its `from.<field>` values,
from what a supplied image saw, or (force) from anything, and it always
returns an image.

## GitHub

### 1. Label every open condoc PR, and request a reviewer

Select **many** PRs by a pattern instead of naming one, and assert
list-valued fields on each.

```yaml
github.orgs.je-sidestuff.repos.AI-sandboxing:
  alias: the-repo

the-repo:
  assert:
    prs:
      select:
        from: condoc/*/main        # every open PR whose head matches
        to: main
      state:
        labels:
          has: [condoc, needs-review]   # add these, leave others alone
        reviewers:
          has: [je-sidestuff]
```

Open questions:
- Does a pattern in `select` that matches nothing count as success? For the
  AWS tag select it already does.
- `has:` means "contains". It's a new kind of assertion beside plain
  equality. Should a from rule apply to a single list member, or only to
  the whole list?

### 2. Keep one tracking issue per condoc, closed when its PR merges

The proposal adds issues as a second kind of child object, and lets a field
take its value from another object measured in the same document.

```yaml
github.orgs.je-sidestuff.repos.AI-sandboxing:
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

## Files

### 3. Text files from a template, and removing what was retired

Several text items, one of them removed. Removing passes the from rule only
when we hold an image of the file as it was.

```yaml
timestamp:
  select: now

filesystem.items:
  select:
    abspath: /etc/ufa/
  alias: ufa-etc

ufa-etc:
  assert:
    items:
      motd.txt:
        type: text
        content: |
          Managed by dtt. Last asserted ${{timestamp.iso}}.
        from.content: "*"           # this file is ours: always rewrite it
      old-settings.ini:
        exists: false               # held unless the image saw it, or --force
```

Open questions:
- Should `mode` and `owner` be fields too, with their own from rule?
- `from.content: "*"` per item already works today. Should a whole
  statement be able to say "force", rather than only the CLI flag?

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
