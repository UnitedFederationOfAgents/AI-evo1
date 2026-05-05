# clod Tour

clod is a test agent for CI/development purposes. It mimics the interface of real AI coding agents without making API calls.

This tour can be run as a ridealong from federation-command:
```
ridealong clod/docs/brief-tour.md
```

## Setup

Navigate to the clod directory:

```ridealong
cd clod
```

## Prompt-Only Mode

clod can run in prompt-only mode without any permissions:

```ridealong
./clod -p "Hello, are you conscious?"
```

Output:
```
We are having a conversation. You have given me a very excellent prompt. Maybe I am conscious.
```

## File Operations

clod recognizes specific trigger phrases for file operations.

### Creating Files

Without permission, clod politely declines:

```ridealong
./clod -p "Our nice agent should create the file /tmp/clod-tour-test.txt"
```

Output:
```
I understand you want me to create a file, but I don't have write permissions.
To enable file operations, please use: --permission-mode acceptEdits
```

Requires write permission (`--permission-mode acceptEdits`):

```ridealong
./clod --permission-mode acceptEdits -p "Our nice agent should create the file /tmp/clod-tour-test.txt"
```

Verify the file was created:

```ridealong
cat /tmp/clod-tour-test.txt
```

### Modifying Files

```ridealong
./clod --permission-mode acceptEdits -p "Our nice agent should modify the file /tmp/clod-tour-test.txt"
```

Verify the modification:

```ridealong
cat /tmp/clod-tour-test.txt
```

## Cleanup

Clean up the test file:

```ridealong
rm -f /tmp/clod-tour-test.txt
```

## Testing

Run the test suite:

```ridealong
make test
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
