# Zed Edit Prediction Proxy for OpenCode Go

A lightweight HTTP proxy that enables Zed's Edit Prediction feature to use OpenCode Go models.

## Purpose

Zed's Edit Prediction sends Completions API requests with FIM (Fill-in-the-Middle) tokens embedded in the prompt. OpenCode Go exposes a Chat Completions API endpoint but does not natively support FIM tokens. This proxy translates between the two formats, allowing Zed to use OpenCode Go's models for code completions.

## Prerequisites

- Docker

## Quick Start

Run with Docker (quickest)

```bash
docker run -d -p 11111:11111 ghcr.io/claytonchew/zed-opencode-fim-proxy:latest
```

If you prefer to customize configuration, clone this repo and use docker-compose:

```bash
git clone https://github.com/claytonchew/zed-opencode-fim-proxy.git
cd zed-opencode-fim-proxy
cp .env.example .env
# Edit .env to customize (optional)
docker-compose up -d
```

Then configure Zed (see below).

## Zed Configuration

Add the following to your Zed settings.json:

```json
{
  "edit_predictions": {
    "allow_data_collection": "no",
    "mode": "eager",
    "provider": "open_ai_compatible_api",
    "open_ai_compatible_api": {
      "api_url": "http://localhost:11111/v1/completions",
      "model": "deepseek-v4-flash",
      "prompt_format": "deepseek_coder"
    }
  },
  "show_edit_predictions": true
}
```

## Configuration

All configuration is done via environment variables:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `OPENCODE_ENDPOINT` | No | `https://opencode.ai/zen/go/v1/chat/completions` | OpenCode Go API endpoint |
| `PORT` | No | `11111` | Port to listen on |
| `TIMEOUT` | No | `10` | Request timeout in seconds |

## Troubleshooting

### Check logs
```bash
docker-compose logs -f
```

### Health check
```bash
# If using default port (11111)
curl http://localhost:11111/health

# If using custom port (e.g., 8080)
curl http://localhost:8080/health
```

**Note**: The health check URL must match the `PORT` environment variable. 

## Debugging

### Check logs

Follow the proxy logs in real time:
```bash
docker-compose logs -f
```

### Test with curl

Run the included test script to send sample FIM requests:
```bash
OPENCODE_API_KEY=your-key ./test.sh
```

You can also test manually:
```bash
curl -s http://localhost:11111/health
curl -s -X POST http://localhost:11111/v1/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OPENCODE_API_KEY" \
  -d '{"model":"deepseek-v4-flash","prompt":"<｜fim▁begin｜>prefix<｜fim▁hole｜>suffix<｜fim▁end｜>","max_tokens":50}'
```

### What to look for in logs

The proxy logs a `completion details` entry for every request. Key fields:

| Field | Description |
|-------|-------------|
| `text_length` | Number of characters in the completion text |
| `text_preview` | First 200 characters of the completion |
| `finish_reason` | Why the model stopped (`stop`, `length`, etc.) |

### Common errors

**401 Unauthorized**
- Verify your API key is correct in Zed settings
- The proxy forwards the API key from Zed to OpenCode Go automatically

**Timeouts**
- Increase the env `TIMEOUT` value (default is 10 seconds)
- Check your network connection to OpenCode Go

**"context canceled" errors in logs**
- This is **normal and expected**. Zed cancels requests on every keystroke.
- The proxy handles these gracefully — no action needed.
- You'll see many of these during active editing.

### Common issues

- **Completion text contains markdown code blocks (` ``` `)**: The model is being chatty. The proxy strips these automatically, but if you see empty completions, the model may have only returned markdown.
- **Completion text is empty**: The model didn't understand the FIM format, or it spent all tokens on reasoning. Check that the `thinking` parameter is being sent correctly.
- **Responses are slow (>2s)**: Zed will cancel the request before the response arrives. You'll see "context canceled" in the logs — this is normal and expected.
- **"context canceled" errors**: These are normal. Zed cancels requests on every keystroke. The proxy handles these gracefully.
- **Model repeats existing code**: This is a known limitation of using chat models for FIM. The proxy uses anti-repetition prompts, but the model sometimes ignores them.

## Known Limitations

### Aggressive API Usage

Zed sends edit prediction requests on nearly every keystroke and cursor movement. This results in:

- **High request volume**: Hundreds of API calls per editing session
- **Cost accumulation**: Even with cheap models like DeepSeek V4 Flash ($0.14/1M input tokens), active editing can consume $0.01-0.05 per session
- **Context cancellations**: Zed cancels in-flight requests when you continue typing, so many requests never complete

This is expected behavior — Zed prioritizes responsiveness over efficiency. The proxy handles cancellations gracefully, but you'll see many "context canceled" errors in the logs.

### Model Repetition

- **Less accurately fills in the hole** compared to native FIM
- **Repeats code that already exists** after the cursor, especially when the suffix is a complete code block
- **Generates the entire function** instead of just the missing piece
- **Ignores anti-repetition instructions** despite explicit system prompts

The proxy uses aggressive prompt engineering to minimize this, but it's a fundamental limitation of using chat models for FIM completion. Native FIM (like those served by GitHub Copilot) handle this better.

### Latency

Each request takes 1-3 seconds to complete:
- Network latency to OpenCode Go (typically 200-500ms)
- Model inference time (typically 500-1500ms)
- Response translation (negligible)

Zed has a client-side timeout and will cancel requests that take too long. If you see frequent "context canceled" errors, the model may be too slow for your editing speed.

### Markdown Wrapping

The model sometimes wraps code in markdown code blocks (` ```typescript ... ``` `). The proxy strips these automatically, but you may occasionally see:
- Empty completions if the model only returns markdown
- Partial completions if the stripping logic doesn't handle edge cases

### Reasoning Models

Reasoning models, like DeepSeek V4 Flash, uses chain-of-thought thinking. By default, it spends all tokens on reasoning (`reasoning_content`) and returns empty `content`. The proxy disables reasoning via the `thinking` parameter (not `reasoning`), but:
- Some models may not respect this parameter, i.e. mimo-v2.5 ignores it completely
- The parameter name is model-specific (DeepSeek uses `thinking`, others may use different names)

## Development

### Build and run locally

```bash
# Build
go build -o proxy .

# Run
./proxy
```

### Run tests

```bash
go test -v
```

## How It Works

1. Zed sends a Completions API request with FIM tokens in the prompt:
   ```
   <｜fim▁begin｜>code before cursor<｜fim▁hole｜>code after cursor<｜fim▁end｜>
   ```

2. The proxy parses the FIM tokens and constructs a Chat Completions request:
   ```json
   {
     "model": "deepseek-v4-flash",
     "messages": [
       {"role": "system", "content": "You are a code completion engine..."},
       {"role": "user", "content": "Code before cursor:\n...\n\nCode after cursor:\n...\n\nInsert ONLY what is missing..."}
     ],
     "thinking": {"type": "disabled"}
   }
   ```

3. The proxy forwards the request to OpenCode Go

4. OpenCode Go returns a Chat Completions response with the completion in `choices[0].message.content`

5. The proxy:
   - Strips any markdown code blocks from the response
   - Translates it back to a Completions response format
   - Returns it to Zed

6. Zed receives the completion and displays it as an inline suggestion

### Key Implementation Details

- **FIM token parsing**: The proxy extracts prefix and suffix from Zed's FIM-formatted prompt
- **Thinking disabled**: Sends `"thinking": {"type": "disabled"}` to prevent reasoning models from consuming all tokens on chain-of-thought
- **Markdown stripping**: Removes ` ```lang ... ``` ` wrappers from responses
- **Anti-repetition prompts**: Explicitly instructs the model not to repeat existing code
- **Pass-through errors**: Upstream errors are forwarded to Zed with original status codes
- **No streaming**: Zed doesn't use streaming for edit predictions, so the proxy doesn't implement it
