package common

// DefaultCustomHeadHTML is the default content injected at the
// `<!--head-html-->` placeholder of the embedded index.html. It is the
// editable region exposed to administrators as the `CustomHeadHTML` option
// (see router/web-router.go); the structural head tags (charset, viewport,
// favicon) and the env-driven `<!--umami-->` / `<!--Google Analytics-->`
// placeholders live outside it in web/index.html.
const DefaultCustomHeadHTML = `    <meta name="generator" content="New API" />

    <!-- Primary Meta Tags -->
    <title>Tokeness - One Entry, All Models | AI API</title>
    <meta name="title" content="Tokeness - One Entry, All Models | AI API" />
    <meta
      name="description"
      content="Tokeness gives developers one entry to every major AI model. Use one OpenAI-compatible key for GPT, Claude, DeepSeek and more, with quota control, routing management, usage audit and privacy-preserving relay."
    />
    <meta
      name="keywords"
      content="AI API Gateway, LLM API, GPT API, Claude API, OpenAI compatible API, AI API proxy, Tokeness, AI API Hub"
    />

    <!-- Open Graph / Facebook -->
    <meta property="og:type" content="website" />
    <meta
      property="og:title"
      content="Tokeness - One Entry, All Models | AI API"
    />
    <meta
      property="og:description"
      content="Tokeness gives developers one entry to every major AI model. Use one OpenAI-compatible key for GPT, Claude, DeepSeek and more, with quota control, routing management, usage audit and privacy-preserving relay."
    />

    <!-- Twitter -->
    <meta
      name="twitter:title"
      content="Tokeness - One Entry, All Models | AI API"
    />
    <meta
      name="twitter:description"
      content="Tokeness gives developers one entry to every major AI model. Use one OpenAI-compatible key for GPT, Claude, DeepSeek and more, with quota control, routing management, usage audit and privacy-preserving relay."
    />

    <meta name="theme-color" content="#fff" />`
