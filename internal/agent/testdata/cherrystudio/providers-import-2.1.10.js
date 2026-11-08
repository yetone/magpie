// Cherry Studio 2.1.10's handling of a cherrystudio://providers link,
// copied from its source (github.com/CherryHQ/cherry-studio, tag v2.1.10):
// ProtocolService.dispatchProtocolUrl's switch on the host, and
// parseProvidersImportData and handleProvidersProtocolUrl from
// src/main/services/protocol/handlers/providersImport.ts with their types
// and logger taken out. Then the renderer's side of
// src/renderer/pages/settings/ProviderSettings/hooks/useProviderDeepLinkImport.ts:
// ImportedProviderSearchDataSchema's checks (zod written out by hand,
// ProviderTypeSchema from src/renderer/types/provider.ts),
// resolveDefaultEndpoint, validateApiHost (src/renderer/utils/api.ts) and
// the createProvider and api-keys calls a new provider gets, with the
// confirm dialog (UrlSchemaInfoPopup) taken as confirmed, baseUrl as its
// apiHost. TestCherryStudioLinkParsedAs2_1_10 runs it with the link as
// argv[2] and reads the JSON it prints.
function parseProvidersImportData(data) {
  try {
    const decoded = Buffer.from(data, 'base64').toString('utf-8')
    let result

    try {
      result = JSON.parse(decoded)
    } catch {
      result = JSON.parse(decoded.replaceAll("'", '"').replaceAll('(', '').replaceAll(')', ''))
    }

    return JSON.stringify(result)
  } catch (error) {
    return null
  }
}

let opened = null
function handleProvidersProtocolUrl(url) {
  switch (url.pathname) {
    case '/api-keys': {
      // replace + and / to _ and - because + and / are processed by URLSearchParams
      const processedSearch = url.search.replaceAll('+', '_').replaceAll('/', '-')
      const params = new URLSearchParams(processedSearch)
      const data = parseProvidersImportData(params.get('data')?.replaceAll('_', '+').replaceAll('-', '/') || '')

      if (!data) {
        return
      }

      opened = { version: params.get('v'), route: `/settings/provider?addProviderData=${encodeURIComponent(data)}` }
      break
    }
    default:
      break
  }
}

const urlObj = new URL(process.argv[2])
if (urlObj.hostname.toLowerCase() === 'providers') handleProvidersProtocolUrl(urlObj)

const ENDPOINT_TYPE = {
  OPENAI_CHAT_COMPLETIONS: 'openai-chat-completions',
  OPENAI_RESPONSES: 'openai-responses',
  ANTHROPIC_MESSAGES: 'anthropic-messages',
  GOOGLE_GENERATE_CONTENT: 'google-generate-content',
  OLLAMA_CHAT: 'ollama-chat'
}
function resolveDefaultEndpoint(type) {
  switch (type) {
    case 'anthropic':
    case 'vertex-anthropic':
      return ENDPOINT_TYPE.ANTHROPIC_MESSAGES
    case 'openai-response':
      return ENDPOINT_TYPE.OPENAI_RESPONSES
    case 'gemini':
    case 'vertexai':
      return ENDPOINT_TYPE.GOOGLE_GENERATE_CONTENT
    case 'ollama':
      return ENDPOINT_TYPE.OLLAMA_CHAT
    default:
      return ENDPOINT_TYPE.OPENAI_CHAT_COMPLETIONS
  }
}
const ProviderTypes = ['openai', 'openai-response', 'anthropic', 'gemini', 'azure-openai', 'vertexai', 'mistral', 'aws-bedrock', 'vertex-anthropic', 'new-api', 'gateway', 'ollama']
const str1 = (v) => typeof v === 'string' && v.length >= 1
function schemaOk(d) {
  return d && typeof d === 'object' && str1(d.id) && str1(d.apiKey) && str1(d.baseUrl) &&
    (d.type === undefined || ProviderTypes.includes(d.type)) &&
    (d.name === undefined || typeof d.name === 'string')
}
function validateApiHost(apiHost) {
  if (!apiHost || !apiHost.trim()) {
    return true
  }
  try {
    const url = new URL(apiHost.trim())
    if (url.protocol !== 'http:' && url.protocol !== 'https:') {
      return false
    }
    return true
  } catch {
    return false
  }
}

const out = { opened, created: null, apiKey: null }
if (opened) {
  const search = new URL('http://x' + opened.route).searchParams.get('addProviderData')
  const providerData = JSON.parse(search)
  if (schemaOk(providerData)) {
    const updatedProvider = { ...providerData, apiHost: providerData.baseUrl || '' }
    const defaultChatEndpoint = resolveDefaultEndpoint(updatedProvider.type)
    if (!updatedProvider.apiHost || validateApiHost(updatedProvider.apiHost)) {
      out.created = {
        providerId: updatedProvider.id,
        name: updatedProvider.name || providerData.id,
        defaultChatEndpoint,
        endpointConfigs: updatedProvider.apiHost ? { [defaultChatEndpoint]: { baseUrl: updatedProvider.apiHost } } : undefined
      }
      if (updatedProvider.apiKey.trim()) out.apiKey = updatedProvider.apiKey.trim()
    }
  }
}
process.stdout.write(JSON.stringify(out))
