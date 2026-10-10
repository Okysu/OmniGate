import { describe, expect, it } from 'vitest'
import { COMPLETIONS_SAMPLE_MODEL, IMAGE_SAMPLE_MODEL, KEY_PLACEHOLDER, modelSnippets, SPEECH_SAMPLE_MODEL, TRANSCRIPTION_SAMPLE_MODEL, usageSnippets } from './snippets'

describe('usageSnippets', () => {
  it('points every sample at <origin>/v1', () => {
    const s = usageSnippets('https://gw.example.com/')
    expect(s.baseUrl).toBe('https://gw.example.com/v1')
    expect(s.curl).toContain('curl https://gw.example.com/v1/chat/completions')
    expect(s.curl).toContain(`Bearer ${KEY_PLACEHOLDER}`)
    expect(s.openai).toContain('base_url="https://gw.example.com/v1"')
    expect(s.openaiNode).toContain('baseURL: \'https://gw.example.com/v1\'')
    // The Anthropic SDK appends /v1/messages itself.
    expect(s.anthropic).toContain('base_url="https://gw.example.com"')
  })

  it('uses the given key', () => {
    expect(usageSnippets('http://localhost:8080', 'og-abc').openai).toContain('api_key="og-abc"')
  })
})

describe('modelSnippets', () => {
  it('uses the model in OpenAI chat and Anthropic messages samples', () => {
    const s = modelSnippets('https://gw.example.com/', 'deepseek-chat')
    expect(s.openaiCurl).toContain('curl https://gw.example.com/v1/chat/completions')
    expect(s.openaiCurl).toContain('"model": "deepseek-chat"')
    expect(s.openaiPython).toContain('model="deepseek-chat"')
    expect(s.openaiPython).toContain('base_url="https://gw.example.com/v1"')
    expect(s.anthropicCurl).toContain('curl https://gw.example.com/v1/messages')
    expect(s.anthropicCurl).toContain(`x-api-key: ${KEY_PLACEHOLDER}`)
    expect(s.anthropicCurl).toContain('"max_tokens": 1024')
    expect(s.anthropicPython).toContain('base_url="https://gw.example.com"')
    expect(s.anthropicPython).toContain('model="deepseek-chat"')
    expect(s.embeddingsCurl).toContain('/v1/embeddings')
  })

  it('escapes model names for JSON, Python and the shell', () => {
    const s = modelSnippets('http://x', 'we"ird\'s')
    expect(s.openaiPython).toContain('model="we\\"ird\'s"')
    // The single quote is closed, escaped and reopened inside the -d '…' body.
    expect(s.openaiCurl).toContain(`"model": "we\\"ird'\\''s"`)
  })
})

describe('image snippets (phase7 §1)', () => {
  it('adds generation (curl + Python) and multipart edit samples to the key page', () => {
    const s = usageSnippets('https://gw.example.com')
    expect(s.imagesCurl).toContain('curl https://gw.example.com/v1/images/generations')
    expect(s.imagesCurl).toContain(`"model": "${IMAGE_SAMPLE_MODEL}"`)
    expect(s.imagesCurl).toContain('-H "Content-Type: application/json"')
    expect(s.imagesPython).toContain('client.images.generate(')
    expect(s.imagesPython).toContain('base_url="https://gw.example.com/v1"')
    expect(s.imageEditCurl).toContain('curl https://gw.example.com/v1/images/edits')
    expect(s.imageEditCurl).toContain('-F image=@input.png')
    expect(s.imageEditCurl).toContain(`-F model=${IMAGE_SAMPLE_MODEL}`)
    // multipart: curl sets the boundary itself, no JSON content type
    expect(s.imageEditCurl).not.toContain('Content-Type')
  })

  it('uses the plaza model and quotes unusual names for the shell', () => {
    const s = modelSnippets('https://gw.example.com', 'dall-e-3')
    expect(s.imagesCurl).toContain('"model": "dall-e-3"')
    expect(s.imagesPython).toContain('model="dall-e-3"')
    expect(s.imageEditCurl).toContain('-F model=dall-e-3')
    const odd = modelSnippets('http://x', 'my model\'s')
    expect(odd.imageEditCurl).toContain(`-F 'model=my model'\\''s'`)
  })
})

describe('audio snippets (phase9 §1)', () => {
  it('adds multipart transcription and speech samples to the key page', () => {
    const s = usageSnippets('https://gw.example.com')
    expect(s.transcriptionCurl).toContain('curl https://gw.example.com/v1/audio/transcriptions')
    expect(s.transcriptionCurl).toContain('-F file=@audio.mp3')
    expect(s.transcriptionCurl).toContain(`-F model=${TRANSCRIPTION_SAMPLE_MODEL}`)
    // multipart: curl sets the boundary itself
    expect(s.transcriptionCurl).not.toContain('Content-Type')
    expect(s.transcriptionPython).toContain('client.audio.transcriptions.create(')
    expect(s.transcriptionPython).toContain('open("audio.mp3", "rb")')
    expect(s.transcriptionPython).toContain('base_url="https://gw.example.com/v1"')
    expect(s.speechCurl).toContain('curl https://gw.example.com/v1/audio/speech')
    expect(s.speechCurl).toContain(`"model": "${SPEECH_SAMPLE_MODEL}"`)
    expect(s.speechCurl).toContain('"voice": "alloy"')
    expect(s.speechCurl).toContain('--output speech.mp3')
    expect(s.speechPython).toContain('client.audio.speech.with_streaming_response.create(')
    expect(s.speechPython).toContain('stream_to_file("speech.mp3")')
  })

  it('uses the plaza model and escapes it', () => {
    const s = modelSnippets('https://gw.example.com', 'whisper-1')
    expect(s.transcriptionCurl).toContain('-F model=whisper-1')
    expect(s.transcriptionPython).toContain('model="whisper-1"')
    expect(s.speechCurl).toContain('"model": "whisper-1"')
    expect(s.speechPython).toContain('model="whisper-1"')
    const odd = modelSnippets('http://x', 'tts\'s')
    expect(odd.transcriptionCurl).toContain(`-F 'model=tts'\\''s'`)
    expect(odd.speechCurl).toContain(`"model": "tts'\\''s"`)
  })
})

describe('completions snippets (phase14)', () => {
  it('adds FIM samples with prompt + suffix to the key page', () => {
    const s = usageSnippets('https://gw.example.com')
    expect(s.completionsCurl).toContain('curl https://gw.example.com/v1/completions')
    expect(s.completionsCurl).toContain(`"model": "${COMPLETIONS_SAMPLE_MODEL}"`)
    // JSON escapes stay escaped (no raw newlines inside the strings).
    expect(s.completionsCurl).toContain('"suffix": "\\n    return a\\n"')
    expect(s.completionsPython).toContain('client.completions.create(')
    expect(s.completionsPython).toContain('suffix="\\n    return a\\n"')
    expect(s.completionsPython).toContain('print(resp.choices[0].text)')
  })

  it('uses the plaza model and escapes it', () => {
    const s = modelSnippets('https://gw.example.com', 'deepseek-flash')
    expect(s.completionsCurl).toContain('"model": "deepseek-flash"')
    expect(s.completionsPython).toContain('model="deepseek-flash"')
    expect(modelSnippets('http://x', 'fim\'s').completionsCurl).toContain(`"model": "fim'\\''s"`)
  })
})
