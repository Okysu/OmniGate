// Code samples for calling the gateway (API Keys page, landing page quick start).

/** Placeholder shown instead of a real key. */
export const KEY_PLACEHOLDER = 'og-你的密钥'

export interface UsageSnippets {
  /** `<origin>/v1`: base URL for OpenAI-compatible SDKs. */
  baseUrl: string
  curl: string
  openai: string
  /** OpenAI Node SDK. */
  openaiNode: string
  anthropic: string
  /** Image generation (`/v1/images/generations`) via curl. */
  imagesCurl: string
  /** Image generation with the OpenAI Python SDK. */
  imagesPython: string
  /** Image edit (`/v1/images/edits`, multipart) via curl. */
  imageEditCurl: string
  /** phase9 §1: transcription (`/v1/audio/transcriptions`, multipart) via curl. */
  transcriptionCurl: string
  /** Transcription with the OpenAI Python SDK. */
  transcriptionPython: string
  /** Speech synthesis (`/v1/audio/speech`) via curl, saved to speech.mp3. */
  speechCurl: string
  /** Speech synthesis with the OpenAI Python SDK, streamed to speech.mp3. */
  speechPython: string
  /** phase14: FIM completion (`/v1/completions` with `suffix`) via curl. */
  completionsCurl: string
  /** FIM completion with the OpenAI Python SDK. */
  completionsPython: string
}

/** Default image model of the generic samples. */
export const IMAGE_SAMPLE_MODEL = 'gpt-image-1'
/** Default transcription / speech models of the generic samples. */
export const TRANSCRIPTION_SAMPLE_MODEL = 'gpt-4o-mini-transcribe'
export const SPEECH_SAMPLE_MODEL = 'gpt-4o-mini-tts'
/** Default model of the generic completions (FIM) samples. */
export const COMPLETIONS_SAMPLE_MODEL = 'deepseek-v4-pro'

// FIM: the model writes the code between prompt and suffix.
function completionsCurlSnippet(baseUrl: string, key: string, modelJson: string): string {
  return `curl ${baseUrl}/completions \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d ${shellJson(`{
    "model": ${modelJson},
    "prompt": "def fib(n):\\n    a, b = 0, 1\\n",
    "suffix": "\\n    return a\\n",
    "max_tokens": 128
  }`)}`
}

function completionsPythonSnippet(baseUrl: string, key: string, modelJson: string): string {
  return `from openai import OpenAI

client = OpenAI(
    base_url="${baseUrl}",
    api_key="${key}",
)

# FIM 补全：模型生成 prompt 与 suffix 之间的代码
resp = client.completions.create(
    model=${modelJson},
    prompt="def fib(n):\\n    a, b = 0, 1\\n",
    suffix="\\n    return a\\n",
    max_tokens=128,
)
print(resp.choices[0].text)`
}

function transcriptionCurlSnippet(baseUrl: string, key: string, model: string): string {
  return `curl ${baseUrl}/audio/transcriptions \\
  -H "Authorization: Bearer ${key}" \\
  -F file=@audio.mp3 \\
  -F ${shellArg(`model=${model}`)} \\
  -F response_format=json`
}

function transcriptionPythonSnippet(baseUrl: string, key: string, modelJson: string): string {
  return `from openai import OpenAI

client = OpenAI(
    base_url="${baseUrl}",
    api_key="${key}",
)

with open("audio.mp3", "rb") as f:
    transcript = client.audio.transcriptions.create(
        model=${modelJson},
        file=f,
    )
print(transcript.text)`
}

function speechCurlSnippet(baseUrl: string, key: string, modelJson: string): string {
  return `curl ${baseUrl}/audio/speech \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d ${shellJson(`{
    "model": ${modelJson},
    "input": "你好，欢迎使用 OmniGate。",
    "voice": "alloy"
  }`)} \\
  --output speech.mp3`
}

function speechPythonSnippet(baseUrl: string, key: string, modelJson: string): string {
  return `from openai import OpenAI

client = OpenAI(
    base_url="${baseUrl}",
    api_key="${key}",
)

# 边接收边写入文件，不必等待整段音频生成完毕
with client.audio.speech.with_streaming_response.create(
    model=${modelJson},
    voice="alloy",
    input="你好，欢迎使用 OmniGate。",
) as response:
    response.stream_to_file("speech.mp3")`
}

function imagesCurlSnippet(baseUrl: string, key: string, modelJson: string): string {
  return `curl ${baseUrl}/images/generations \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d ${shellJson(`{
    "model": ${modelJson},
    "prompt": "一只在月球上喝咖啡的柴犬，插画风格",
    "size": "1024x1024",
    "n": 1
  }`)}`
}

function imagesPythonSnippet(baseUrl: string, key: string, modelJson: string): string {
  return `import base64
from openai import OpenAI

client = OpenAI(
    base_url="${baseUrl}",
    api_key="${key}",
)

resp = client.images.generate(
    model=${modelJson},
    prompt="一只在月球上喝咖啡的柴犬，插画风格",
    size="1024x1024",
)
# gpt-image-* 返回 b64_json；dall-e-* 默认返回 url
image = resp.data[0]
if image.b64_json:
    with open("output.png", "wb") as f:
        f.write(base64.b64decode(image.b64_json))
else:
    print(image.url)`
}

function imageEditCurlSnippet(baseUrl: string, key: string, model: string): string {
  return `curl ${baseUrl}/images/edits \\
  -H "Authorization: Bearer ${key}" \\
  -F ${shellArg(`model=${model}`)} \\
  -F image=@input.png \\
  -F "prompt=把背景换成星空" \\
  -F size=1024x1024`
}

/** Samples for a gateway served at `origin` (e.g. `window.location.origin`). */
export function usageSnippets(origin: string, key: string = KEY_PLACEHOLDER): UsageSnippets {
  const base = origin.replace(/\/+$/, '')
  const baseUrl = `${base}/v1`
  return {
    baseUrl,
    curl: `curl ${baseUrl}/chat/completions \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "你好"}]
  }'`,
    openai: `from openai import OpenAI

client = OpenAI(
    base_url="${baseUrl}",
    api_key="${key}",
)

resp = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)`,
    openaiNode: `import OpenAI from 'openai'

const client = new OpenAI({
  baseURL: '${baseUrl}',
  apiKey: '${key}',
})

const resp = await client.chat.completions.create({
  model: 'gpt-4o-mini',
  messages: [{ role: 'user', content: '你好' }],
})
console.log(resp.choices[0].message.content)`,
    anthropic: `from anthropic import Anthropic

# Anthropic SDK 会自动拼接 /v1/messages，因此 base_url 不带 /v1
client = Anthropic(
    base_url="${base}",
    api_key="${key}",
)

msg = client.messages.create(
    model="claude-sonnet-4-5",
    max_tokens=1024,
    messages=[{"role": "user", "content": "你好"}],
)
print(msg.content[0].text)`,
    imagesCurl: imagesCurlSnippet(baseUrl, key, q(IMAGE_SAMPLE_MODEL)),
    imagesPython: imagesPythonSnippet(baseUrl, key, q(IMAGE_SAMPLE_MODEL)),
    imageEditCurl: imageEditCurlSnippet(baseUrl, key, IMAGE_SAMPLE_MODEL),
    transcriptionCurl: transcriptionCurlSnippet(baseUrl, key, TRANSCRIPTION_SAMPLE_MODEL),
    transcriptionPython: transcriptionPythonSnippet(baseUrl, key, q(TRANSCRIPTION_SAMPLE_MODEL)),
    speechCurl: speechCurlSnippet(baseUrl, key, q(SPEECH_SAMPLE_MODEL)),
    speechPython: speechPythonSnippet(baseUrl, key, q(SPEECH_SAMPLE_MODEL)),
    completionsCurl: completionsCurlSnippet(baseUrl, key, q(COMPLETIONS_SAMPLE_MODEL)),
    completionsPython: completionsPythonSnippet(baseUrl, key, q(COMPLETIONS_SAMPLE_MODEL)),
  }
}

export interface ModelSnippets {
  /** OpenAI Chat Completions via curl. */
  openaiCurl: string
  /** OpenAI Python SDK. */
  openaiPython: string
  /** Anthropic Messages via curl. */
  anthropicCurl: string
  /** Anthropic Python SDK. */
  anthropicPython: string
  /** OpenAI Embeddings via curl. */
  embeddingsCurl: string
  /** OpenAI Images generation via curl. */
  imagesCurl: string
  /** OpenAI Images generation (Python SDK). */
  imagesPython: string
  /** OpenAI Images edit (multipart) via curl. */
  imageEditCurl: string
  /** phase9 §1: audio transcription (multipart) via curl / Python SDK. */
  transcriptionCurl: string
  transcriptionPython: string
  /** phase9 §1: speech synthesis via curl / Python SDK (saved to speech.mp3). */
  speechCurl: string
  speechPython: string
  /** phase14: FIM completion (`/v1/completions` with `suffix`) via curl / Python SDK. */
  completionsCurl: string
  completionsPython: string
}

/** JSON string literal for embedding a model name into code samples. */
function q(s: string): string {
  return JSON.stringify(s)
}

/** Shell-safe single-quoted JSON body (single quotes in the model name are escaped). */
function shellJson(body: string): string {
  return `'${body.replace(/'/g, `'\\''`)}'`
}

/** Single-quoted shell argument; plain `[A-Za-z0-9._:/=@+-]` words stay unquoted. */
function shellArg(v: string): string {
  return /^[\w.:/=@+-]+$/.test(v) ? v : shellJson(v)
}

/** Samples calling one model of the plaza (model detail sheet). */
export function modelSnippets(origin: string, model: string, key: string = KEY_PLACEHOLDER): ModelSnippets {
  const base = origin.replace(/\/+$/, '')
  const baseUrl = `${base}/v1`
  return {
    openaiCurl: `curl ${baseUrl}/chat/completions \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d ${shellJson(`{
    "model": ${q(model)},
    "messages": [{"role": "user", "content": "你好"}]
  }`)}`,
    openaiPython: `from openai import OpenAI

client = OpenAI(
    base_url="${baseUrl}",
    api_key="${key}",
)

resp = client.chat.completions.create(
    model=${q(model)},
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)`,
    anthropicCurl: `curl ${baseUrl}/messages \\
  -H "x-api-key: ${key}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d ${shellJson(`{
    "model": ${q(model)},
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "你好"}]
  }`)}`,
    anthropicPython: `from anthropic import Anthropic

# Anthropic SDK 会自动拼接 /v1/messages，因此 base_url 不带 /v1
client = Anthropic(
    base_url="${base}",
    api_key="${key}",
)

msg = client.messages.create(
    model=${q(model)},
    max_tokens=1024,
    messages=[{"role": "user", "content": "你好"}],
)
print(msg.content[0].text)`,
    embeddingsCurl: `curl ${baseUrl}/embeddings \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d ${shellJson(`{
    "model": ${q(model)},
    "input": "你好"
  }`)}`,
    imagesCurl: imagesCurlSnippet(baseUrl, key, q(model)),
    imagesPython: imagesPythonSnippet(baseUrl, key, q(model)),
    imageEditCurl: imageEditCurlSnippet(baseUrl, key, model),
    transcriptionCurl: transcriptionCurlSnippet(baseUrl, key, model),
    transcriptionPython: transcriptionPythonSnippet(baseUrl, key, q(model)),
    speechCurl: speechCurlSnippet(baseUrl, key, q(model)),
    speechPython: speechPythonSnippet(baseUrl, key, q(model)),
    completionsCurl: completionsCurlSnippet(baseUrl, key, q(model)),
    completionsPython: completionsPythonSnippet(baseUrl, key, q(model)),
  }
}
