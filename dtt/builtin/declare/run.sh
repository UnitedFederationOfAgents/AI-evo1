#!/bin/sh
# declare -- a built-in declarative tool (see AI-evo1/dtt/README.md). dtt's
# own engine does the work, for every provider: it reads this run's inputs
# as JSON on stdin and writes image.json and $DTT_OUTPUT.
exec "${DTT_BIN:-dtt}" tool-engine declare
