# The aws provider's one call into AWS (see declare_aws.go), through boto3.
# dtt runs it with python, a JSON request on stdin:
#
#   {"region": "us-east-1", "service": "ec2", "operation": "describe_vpcs",
#    "params": {"VpcIds": ["vpc-1"]}}
#
# and it prints the response as JSON (every page of it, when the operation
# pages), or, failing, what went wrong on stderr. Credentials and the default
# region come from wherever boto3 finds them.
import json
import sys

try:
    import boto3
    from botocore.exceptions import ClientError
except ImportError as e:
    sys.stderr.write("boto3 isn't installed for %s (%s)" % (sys.executable, e))
    sys.exit(3)

req = json.load(sys.stdin)
try:
    client = boto3.session.Session(region_name=req.get("region") or None).client(req["service"])
    op, params = req["operation"], req.get("params") or {}
    if client.can_paginate(op):
        out = client.get_paginator(op).paginate(**params).build_full_result()
    else:
        out = getattr(client, op)(**params)
except ClientError as e:
    err = e.response.get("Error", {})
    sys.stderr.write("%s: %s" % (err.get("Code", "error"), err.get("Message", str(e))))
    sys.exit(1)
except Exception as e:
    sys.stderr.write(str(e))
    sys.exit(1)
out.pop("ResponseMetadata", None)
json.dump(out, sys.stdout, default=str)
