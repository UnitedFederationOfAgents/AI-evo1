# clod Tour

clod is a test agent for CI/development purposes. It mimics the interface of real AI coding agents without making API calls.

## Prompt-Only Mode

clod can run in prompt-only mode without any permissions:

```bash
./clod -p "Hello, are you conscious?"
```

Output:
```
We are having a conversation. You have given me a very excellent prompt. Maybe I am conscious.
```

## File Operations

clod recognizes specific trigger phrases for file operations.

### Creating Files

Requires write permission (`--permission-mode acceptEdits`):

```bash
./clod --permission-mode acceptEdits -p "Our nice agent should create the file /tmp/test.txt"
```

Without permission, clod politely declines:

```bash
./clod -p "Our nice agent should create the file /tmp/test.txt"
```

Output:
```
I understand you want me to create a file, but I don't have write permissions.
To enable file operations, please use: --permission-mode acceptEdits
```

### Modifying Files

```bash
./clod --permission-mode acceptEdits -p "Our nice agent should modify the file /tmp/test.txt"
```

## Testing

Run the test suite:

```bash
make test
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
