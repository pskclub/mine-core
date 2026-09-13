# AI · ตั้งค่าแต่ละ Provider

| Provider | `AI_PROVIDER` | generate | embed |
|---|---|---|---|
| Anthropic (Claude) | `anthropic` | ✅ | ❌ *(ไม่มี API)* |
| OpenAI | `openai` | ✅ | ✅ |
| Google Gemini | `google` | ✅ | ✅ |
| Ollama (ในเครื่อง) | `ollama` | ✅ | ✅ |
| OpenAI-compatible อื่น ๆ | `compat` | ✅ | ✅ |
| Vertex AI · Bedrock · Azure | — | ⚠️ ผ่าน [escape hatch](#vertex-ai-bedrock-azure) | |

---

## Anthropic (Claude)

**คีย์:** [console.anthropic.com](https://console.anthropic.com/settings/keys) → API Keys → Create Key (ขึ้นต้น `sk-ant-`)

```sh
APP_AI_PROVIDER=anthropic
APP_AI_MODEL=claude-opus-5
APP_AI_API_KEY=sk-ant-api03-...
```

| model              | เหมาะกับ                                 |
| --------------------| ------------------------------------------|
| `claude-opus-5`    | งานยาก งาน agentic ยาว ๆ                 |
| `claude-sonnet-5`  | สมดุลราคา/คุณภาพ — ใช้เป็นค่าเริ่มต้นได้ |
| `claude-haiku-4-5` | จัดหมวด คัดกรอง งานที่เน้นเร็ว/ถูก       |

ดูรายการปัจจุบัน:

```sh
curl -s https://api.anthropic.com/v1/models \
  -H "x-api-key: $KEY" -H "anthropic-version: 2023-06-01" \
  | jq -r '.data[].id'
```

**สิ่งที่ใช้ได้ครบ:** streaming · structured output · tool use · vision & PDF · **prompt caching** (`CacheSystem: true`) · **reasoning** ทุกระดับ

⚠️ **ไม่มี embedding API** — service ที่ generate ด้วย Claude ต้องตั้งฝั่ง embed เป็นเจ้าอื่น:

```sh
APP_AI_PROVIDER=anthropic
APP_AI_MODEL=claude-opus-5
APP_AI_API_KEY=sk-ant-...

APP_AI_EMBED_PROVIDER=openai              # คนละเจ้า
APP_AI_EMBED_MODEL=text-embedding-3-small
APP_AI_EMBED_API_KEY=sk-...               # คนละคีย์
```

ตั้ง `AI_EMBED_PROVIDER=anthropic` จะ error ตั้งแต่ boot พร้อมบอกให้เปลี่ยน

---

## OpenAI

**คีย์:** [platform.openai.com/api-keys](https://platform.openai.com/api-keys) (ขึ้นต้น `sk-`)

```sh
APP_AI_PROVIDER=openai
APP_AI_MODEL=gpt-5
APP_AI_API_KEY=sk-...

APP_AI_EMBED_MODEL=text-embedding-3-small   # ใช้คีย์เดียวกันอัตโนมัติ
```

ดูรายการ model ที่บัญชีคุณเข้าถึงได้จริง — **ดีกว่าลอกจากเอกสารเพราะสิทธิ์ต่างกันตามบัญชี**:

```sh
curl -s https://api.openai.com/v1/models -H "Authorization: Bearer $KEY" \
  | jq -r '.data[].id' | sort
```

**สิ่งที่ใช้ได้:** streaming · structured output · tool use · vision · **reasoning** (`reasoning_effort`) · embeddings

⚠️ **`LLMReasoningOff` ใช้ไม่ได้** — reasoning model ของ OpenAI คิดเสมอ ไม่มีปุ่มปิด จะได้ `LLM_UNSUPPORTED` แทนที่จะเงียบ ๆ ส่งไปแล้วบอกว่าปิดแล้ว

### Azure OpenAI

ใช้ provider `openai` แล้วชี้ base URL ไปที่ deployment:

```sh
APP_AI_PROVIDER=openai
APP_AI_BASE_URL=https://<resource>.openai.azure.com/openai/deployments/<deployment>
APP_AI_MODEL=<deployment>
APP_AI_API_KEY=<azure key>
```

ถ้า Azure ต้องการ `api-version` หรือ header เฉพาะ ต้องใช้ [escape hatch](#vertex-ai-bedrock-azure) เพราะ driver ยังไม่ได้ต่อ option พวกนั้นออกมา

---

## Google Gemini

**คีย์:** [aistudio.google.com/apikey](https://aistudio.google.com/apikey) (ขึ้นต้น `AIza`) — ฟรีมีโควตาให้ลองได้

```sh
APP_AI_PROVIDER=google
APP_AI_MODEL=gemini-2.5-flash
APP_AI_API_KEY=AIza...

APP_AI_EMBED_MODEL=gemini-embedding-001   # 3072 มิติถ้าไม่บอกอะไร
APP_AI_EMBED_DIMENSIONS=768               # ขอความยาวที่ตรงกับคอลัมน์
```

| model | เหมาะกับ |
|---|---|
| `gemini-2.5-flash` | ค่าเริ่มต้นที่ดี — เร็ว ถูก อ่านรูปได้ |
| `gemini-2.5-pro` | งานที่ต้องการคุณภาพสูงกว่า |
| `gemini-flash-lite-latest` | งานปริมาณมากที่เน้นถูก — alias ที่ตามรุ่นล่าสุดให้เอง |
| `gemini-3-flash-preview` · `gemini-3-pro-preview` | ตระกูล 3 (ใช้ `thinkingLevel`) |

alias `*-latest` ใช้ `Reasoning` ได้ตามปกติ — ตรวจกับของจริงแล้ว `gemini-flash-latest` → `gemini-3.6-flash`, `gemini-flash-lite-latest` → `gemini-3.5-flash-lite`, `gemini-pro-latest` → `gemini-3.1-pro-preview` ทั้งสามรับ `thinkingLevel`

> ต้องการล็อกพฤติกรรมให้นิ่งข้ามการ deploy ให้ตั้งเป็น id ที่ระบุเวอร์ชัน — alias ขยับตาม Google โดยที่ config ไม่เปลี่ยน ซึ่งแปลว่าคุณภาพ ราคา และ latency ขยับตามไปด้วย
| `gemini-embedding-001` | embeddings |

ดูรายการปัจจุบัน:

```sh
curl -s -H "x-goog-api-key: $KEY" \
  https://generativelanguage.googleapis.com/v1beta/models | jq -r '.models[].name'
```

**สิ่งที่ใช้ได้:** streaming · structured output · tool use · vision & PDF · embeddings (พร้อม `Dimensions`/`Task`) · reasoning · [Google Search grounding, URL context, code execution](./ai-tools.md#tool-ที่-provider-รันเอง)

`Reasoning` map ตามตระกูลของ model — `gemini-3*` ใช้ `thinkingLevel`, `gemini-2.5*` ใช้ `thinkingBudget` ส่วน Gemma และ Gemini 1.5/2.0 ไม่มีการคิดจึงคืน `LLM_UNSUPPORTED` ([รายละเอียด](./ai-providers.md#reasoning))

```go
resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    Messages:  msgs,
    Reasoning: core.LLMReasoningHigh,
    Tools:     []core.LLMTool{goai.GoogleSearch()},   // ground ด้วยผลค้นหา
})
resp.Sources   // สิ่งที่คำตอบอ้างอิง — ต้องแสดงให้ผู้ใช้เห็น
```

⚠️ ตั้ง `APP_AI_EMBED_DIMENSIONS` ให้ตรงกับคอลัมน์ — `gemini-embedding-001` คืน 3072 มิติถ้าไม่ขออย่างอื่น

---

## Ollama (ในเครื่อง)

ไม่ต้องมีคีย์ ไม่ต้องมีเน็ต เหมาะกับ dev และ CI

```sh
ollama pull llama3.2
ollama pull nomic-embed-text
```

```sh
APP_AI_PROVIDER=ollama
APP_AI_BASE_URL=http://127.0.0.1:11434
APP_AI_MODEL=llama3.2

APP_AI_EMBED_MODEL=nomic-embed-text
```

⚠️ **structured output อ่อนกว่าเจ้าใหญ่มาก** — ถ้า `llm.New[T]` พังบ่อยให้เปิด [`Lenient()`](./ai-typed.md#model-ที่-json-mode-ไม่แข็งแรง) และ **`Reasoning` ไม่รองรับ**

---

## เจ้าอื่นที่พูด OpenAI protocol

Groq · DeepSeek · OpenRouter · Together · Fireworks · Cerebras · vLLM · LiteLLM — ใช้ `compat` + `AI_BASE_URL`

```sh
# Groq
APP_AI_PROVIDER=compat
APP_AI_BASE_URL=https://api.groq.com/openai/v1
APP_AI_MODEL=llama-3.3-70b-versatile
APP_AI_API_KEY=gsk_...
```

```sh
# DeepSeek
APP_AI_BASE_URL=https://api.deepseek.com/v1
APP_AI_MODEL=deepseek-chat
```

```sh
# OpenRouter — ใช้ model ของเจ้าไหนก็ได้ผ่านทางเดียว
APP_AI_BASE_URL=https://openrouter.ai/api/v1
APP_AI_MODEL=anthropic/claude-opus-5
```

```sh
# vLLM / LiteLLM ที่รันเอง
APP_AI_BASE_URL=http://10.0.0.7:8000/v1
APP_AI_MODEL=<ชื่อที่ serve ไว้>
```

`compat` **ต้องมี `AI_BASE_URL`** ไม่งั้น error ตั้งแต่ boot

---

## Vertex AI · Bedrock · Azure {#vertex-ai-bedrock-azure}

**ยังไม่ได้ต่อเข้า driver** เพราะต้องมี config เพิ่มคนละแบบ (project/location, region + SigV4, deployment + api-version) — เป็นงานที่ตั้งใจแยกไว้

ระหว่างนี้ใช้ `goai.NewModel` ประกอบเองแล้วลงทะเบียนตามปกติ ทุกอย่างที่เหลือ (error mapping, metric, log, memory model) ยังทำงานเหมือนเดิม:

```go
import (
    "github.com/zendev-sh/goai/provider/vertex"
    "github.com/pskclub/mine-core/v2/llm/goai"
)

model := goai.NewModel("vertex", vertex.Chat("gemini-2.5-pro",
    vertex.WithProject(cfg.GCPProject),
    vertex.WithLocation("asia-southeast1"),
    vertex.WithTokenSource(ts),      // ADC / service account
))

app, _ := core.NewApp(env, core.WithLLM(model))
```

```go
import "github.com/zendev-sh/goai/provider/bedrock"

model := goai.NewModel("bedrock", bedrock.Chat("anthropic.claude-opus-5",
    bedrock.WithRegion("ap-southeast-1"),
    bedrock.WithAccessKey(id), bedrock.WithSecretKey(secret),
))
```

> `NewModel` ตั้งค่า default (timeout 120s, max tokens 4096, retry 2) ให้เอง ปรับได้ด้วย `goai.WithTimeout(...)` ฯลฯ

---

## ⚠️ กับดัก: env ของ provider ถูกอ่านเอง

ถ้า `APP_AI_API_KEY` **ไม่ได้ตั้ง** ตัว SDK ข้างล่างจะไปหยิบ env มาตรฐานของ provider นั้นมาใช้เอง:

| provider | env ที่มันอ่าน |
|---|---|
| anthropic | `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL` |
| openai | `OPENAI_API_KEY`, `OPENAI_BASE_URL` |
| google | `GEMINI_API_KEY`, `GOOGLE_GENERATIVE_AI_API_KEY` |

**บนเครื่อง dev ที่มี `ANTHROPIC_API_KEY` อยู่แล้ว service จะวิ่งด้วยคีย์นั้นโดยไม่มีอะไรบอก** — บิลไปลงบัญชีส่วนตัว หรือแย่กว่านั้นคือใช้โควตาของ production

ตั้ง `APP_AI_API_KEY` ให้ชัดเจนเสมอ และถ้าอยากกันไว้แน่น ๆ ให้ unset ตัวที่ไม่เกี่ยวในไฟล์ deploy

---

## ตรวจว่าตั้งถูกไหม

boot log บอกทันทีว่า process ผูกกับอะไร:

```
INFO app ready  env=prod service=api llm=anthropic/claude-opus-5 embedder=openai/text-embedding-3-small
```

`llm=false` แปลว่าไม่ได้ตั้ง `AI_PROVIDER` หรือ `AI_MODEL`

ยิงจริงหนึ่งครั้ง:

```go
resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    Messages:  []core.LLMMessage{core.LLMUser("reply with the word: ok")},
    MaxTokens: 16,
})
fmt.Println(resp.Text, resp.Usage.InputTokens, err)
```

หรือรัน integration test ที่มีอยู่แล้วกับคีย์ของคุณ:

```sh
cd v2
APP_AI_PROVIDER=google APP_AI_MODEL=gemini-2.5-flash \
  APP_AI_EMBED_MODEL=gemini-embedding-001 \
  APP_AI_API_KEY=... go test -tags=integration ./llm/goai/
```

มัน**ไม่ได้ผูกกับเจ้าไหนเป็นพิเศษ** — เปลี่ยน env แล้วรันกับ provider ที่คุณจะใช้จริงได้เลย ครอบทั้ง generate, stream, structured output, tool loop, approval gate, vision และ embeddings

---

## Error ที่เจอบ่อยตอนตั้งค่า

| อาการ | สาเหตุ |
|---|---|
| `LLM_DISABLED` (503) | ไม่ได้ตั้ง `AI_PROVIDER` หรือ `AI_MODEL` |
| `LLM_INVALID_CONFIG` ตอน boot | สะกด provider ผิด หรือ `compat` ไม่มี `AI_BASE_URL` |
| `LLM_UNAUTHORIZED` (401/403) | คีย์ผิด หมดอายุ หรือไม่มีสิทธิ์ใช้ model นั้น |
| `LLM_MODEL_NOT_FOUND` (404) | model id ผิด — ใช้คำสั่ง list ด้านบนดูของจริง |
| `LLM_REQUEST_REJECTED` (400) | Gemini คืน 400 เมื่อคีย์ผิดด้วย · หรือ model ไม่รองรับสิ่งที่ขอ (เช่นอ่านรูปไม่เป็น) |
| `LLM_RATE_LIMITED` (429) | โควตาเต็ม — retry ได้ ตั้ง `AI_MAX_RETRIES` |
| `EMBED_INVALID_CONFIG` ตอน boot | ตั้ง `AI_EMBED_PROVIDER=anthropic` ซึ่งไม่มี embedding API |
| ทำงานทั้งที่ไม่ได้ตั้งคีย์ | env ของ provider ถูกอ่านเอง — ดูหัวข้อกับดักด้านบน |

---

ต่อไป: [Overview](./ai.md) · [Text Generation](./ai-text.md) · [Providers & Tuning](./ai-providers.md)
