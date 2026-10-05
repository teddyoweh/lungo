// Package apikeys knows the API keys people commonly keep (OpenAI, Anthropic, Gemini…):
// the environment variable each tool reads, a logo, and a cheap request that tells whether a
// key works. Values live in the OS keychain; machines get them as environment variables.
package apikeys

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"skybuild/internal/secret"
)

// Provider is a service that hands out API keys.
type Provider struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Env   string `json:"env"`  // variable tools read
	Icon  string `json:"icon"` // brand icon in the desktop app ("" = generic key)
	Docs  string `json:"docs"` // where to make a key
	test  string // URL that answers 2xx for a working key
	auth  string // bearer | x-api-key | xi-api-key | query
}

// Providers is the catalogue shown when adding a key. Any other NAME works too.
var Providers = []Provider{
	{"openai", "OpenAI", "OPENAI_API_KEY", "openai", "https://platform.openai.com/api-keys", "https://api.openai.com/v1/models", "bearer"},
	{"anthropic", "Anthropic API", "ANTHROPIC_API_KEY", "anthropic", "https://console.anthropic.com/settings/keys", "https://api.anthropic.com/v1/models", "x-api-key"},
	{"gemini", "Google Gemini", "GEMINI_API_KEY", "gemini", "https://aistudio.google.com/apikey", "https://generativelanguage.googleapis.com/v1beta/models", "query"},
	{"openrouter", "OpenRouter", "OPENROUTER_API_KEY", "openrouter", "https://openrouter.ai/keys", "https://openrouter.ai/api/v1/key", "bearer"},
	{"groq", "Groq", "GROQ_API_KEY", "", "https://console.groq.com/keys", "https://api.groq.com/openai/v1/models", "bearer"},
	{"mistral", "Mistral", "MISTRAL_API_KEY", "mistral", "https://console.mistral.ai/api-keys", "https://api.mistral.ai/v1/models", "bearer"},
	{"xai", "xAI", "XAI_API_KEY", "xai", "https://console.x.ai", "https://api.x.ai/v1/models", "bearer"},
	{"deepseek", "DeepSeek", "DEEPSEEK_API_KEY", "deepseek", "https://platform.deepseek.com/api_keys", "https://api.deepseek.com/models", "bearer"},
	{"perplexity", "Perplexity", "PERPLEXITY_API_KEY", "perplexity", "https://www.perplexity.ai/settings/api", "", ""},
	{"together", "Together AI", "TOGETHER_API_KEY", "", "https://api.together.ai/settings/api-keys", "https://api.together.xyz/v1/models", "bearer"},
	{"fireworks", "Fireworks", "FIREWORKS_API_KEY", "", "https://fireworks.ai/account/api-keys", "https://api.fireworks.ai/inference/v1/models", "bearer"},
	{"cohere", "Cohere", "COHERE_API_KEY", "", "https://dashboard.cohere.com/api-keys", "https://api.cohere.com/v1/models", "bearer"},
	{"nvidia", "NVIDIA NIM", "NVIDIA_API_KEY", "nvidia", "https://build.nvidia.com", "", ""},
	{"replicate", "Replicate", "REPLICATE_API_TOKEN", "replicate", "https://replicate.com/account/api-tokens", "https://api.replicate.com/v1/account", "bearer"},
	{"huggingface", "Hugging Face", "HF_TOKEN", "huggingface", "https://huggingface.co/settings/tokens", "https://huggingface.co/api/whoami-v2", "bearer"},
	{"elevenlabs", "ElevenLabs", "ELEVENLABS_API_KEY", "elevenlabs", "https://elevenlabs.io/app/settings/api-keys", "https://api.elevenlabs.io/v1/user", "xi-api-key"},
	{"exa", "Exa", "EXA_API_KEY", "", "https://dashboard.exa.ai/api-keys", "", ""},
	{"tavily", "Tavily", "TAVILY_API_KEY", "", "https://app.tavily.com", "", ""},
	{"firecrawl", "Firecrawl", "FIRECRAWL_API_KEY", "", "https://www.firecrawl.dev/app/api-keys", "", ""},
	{"pinecone", "Pinecone", "PINECONE_API_KEY", "pinecone", "https://app.pinecone.io", "", ""},
	{"resend", "Resend", "RESEND_API_KEY", "resend", "https://resend.com/api-keys", "https://api.resend.com/domains", "bearer"},
	{"stripe", "Stripe", "STRIPE_SECRET_KEY", "stripe", "https://dashboard.stripe.com/apikeys", "https://api.stripe.com/v1/balance", "bearer"},
	{"github", "GitHub token", "GITHUB_TOKEN", "github", "https://github.com/settings/tokens", "https://api.github.com/user", "bearer"},
	{"vercel", "Vercel", "VERCEL_TOKEN", "vercel", "https://vercel.com/account/tokens", "https://api.vercel.com/v2/user", "bearer"},
	{"cloudflare", "Cloudflare", "CLOUDFLARE_API_TOKEN", "cloudflare", "https://dash.cloudflare.com/profile/api-tokens", "https://api.cloudflare.com/client/v4/user/tokens/verify", "bearer"},
	{"sentry", "Sentry", "SENTRY_AUTH_TOKEN", "sentry", "https://sentry.io/settings/account/api/auth-tokens/", "", ""},
	{"linear", "Linear", "LINEAR_API_KEY", "linear", "https://linear.app/settings/api", "", ""},
	{"notion", "Notion", "NOTION_API_KEY", "notion", "https://www.notion.so/my-integrations", "", ""},
	{"supabase", "Supabase", "SUPABASE_ACCESS_TOKEN", "supabase", "https://supabase.com/dashboard/account/tokens", "", ""},
	{"slack", "Slack", "SLACK_BOT_TOKEN", "slack", "https://api.slack.com/apps", "", ""},
	{"discord", "Discord", "DISCORD_BOT_TOKEN", "discord", "https://discord.com/developers/applications", "", ""},
}

// ForName finds the provider for a variable name (exact match, then a prefix guess).
func ForName(name string) (Provider, bool) {
	for _, p := range Providers {
		if p.Env == name {
			return p, true
		}
	}
	guesses := map[string]string{
		"OPENAI_": "openai", "ANTHROPIC_": "anthropic", "GEMINI_": "gemini", "GOOGLE_API_KEY": "gemini",
		"GOOGLE_GENERATIVE": "gemini", "OPENROUTER_": "openrouter", "GROQ_": "groq", "MISTRAL_": "mistral",
		"XAI_": "xai", "GROK_": "xai", "DEEPSEEK_": "deepseek", "PERPLEXITY_": "perplexity", "PPLX_": "perplexity",
		"TOGETHER_": "together", "FIREWORKS_": "fireworks", "COHERE_": "cohere", "CO_API_KEY": "cohere",
		"NVIDIA_": "nvidia", "REPLICATE_": "replicate", "HF_": "huggingface", "HUGGINGFACE": "huggingface",
		"ELEVEN": "elevenlabs", "EXA_": "exa", "TAVILY_": "tavily", "FIRECRAWL_": "firecrawl",
		"PINECONE_": "pinecone", "RESEND_": "resend", "STRIPE_": "stripe", "GITHUB_": "github", "GH_TOKEN": "github",
		"VERCEL_": "vercel", "CLOUDFLARE_": "cloudflare", "CF_API": "cloudflare", "SENTRY_": "sentry",
		"LINEAR_": "linear", "NOTION_": "notion", "SUPABASE_": "supabase", "SLACK_": "slack", "DISCORD_": "discord",
	}
	for prefix, id := range guesses {
		if strings.HasPrefix(name, prefix) {
			return ByID(id)
		}
	}
	return Provider{}, false
}

// ByID finds a provider.
func ByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// CanTest reports whether Test can check this provider's keys.
func (p Provider) CanTest() bool { return p.test != "" }

var keyPrefix = regexp.MustCompile(`^[a-zA-Z]{2,5}([-_][a-zA-Z]{2,5})?[-_]`)

var validName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// ValidName checks an environment variable name.
func ValidName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("%q isn't a valid variable name: use CAPITALS, digits and _, like OPENAI_API_KEY", name)
	}
	return nil
}

const prefix = "apikey:"

// Get returns a stored key's value.
func Get(name string) string { return secret.Get(prefix + name) }

// Set stores a key's value ("" deletes it).
func Set(name, value string) error { return secret.Set(prefix+name, value) }

// Mask shows enough of a key to recognise it.
func Mask(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return ""
	case len(v) <= 10:
		return strings.Repeat("•", 6)
	}
	head := 4
	if m := keyPrefix.FindString(v); m != "" && len(m) <= 10 {
		head = len(m) // keep a readable prefix like "sk-proj-" or "xai-", never key material
	}
	return v[:head] + "…" + v[len(v)-4:]
}

// Test checks a key against its provider. ok is true for a working key; detail explains.
func Test(ctx context.Context, p Provider, key string) (ok bool, detail string, err error) {
	if p.test == "" {
		return false, "", fmt.Errorf("sky can't test %s keys yet", p.Label)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	url := p.test
	if p.auth == "query" {
		url += "?key=" + key
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, "", err
	}
	switch p.auth {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+key)
	case "x-api-key":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "xi-api-key":
		req.Header.Set("xi-api-key", key)
	}
	req.Header.Set("User-Agent", "skybuild")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("couldn't reach %s: %w", p.Label, err)
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, "works", nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return false, fmt.Sprintf("rejected by %s (HTTP %d)", p.Label, resp.StatusCode), nil
	case resp.StatusCode == 429:
		return true, "works (rate limited right now)", nil
	}
	return false, fmt.Sprintf("%s answered HTTP %d", p.Label, resp.StatusCode), nil
}
