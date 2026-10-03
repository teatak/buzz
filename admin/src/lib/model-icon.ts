import type { Model, ModelPreset } from "../types/admin";

export const modelIconOptions = [
  { value: "openai", label: "OpenAI" },
  { value: "claude", label: "Claude" },
  { value: "gemini", label: "Gemini" },
  { value: "deepseek", label: "DeepSeek" },
  { value: "qwen", label: "Qwen" },
  { value: "moonshot", label: "Kimi" },
  { value: "zhipu", label: "GLM" },
  { value: "mimo", label: "MiMo" },
  { value: "grok", label: "Grok" },
  { value: "openrouter", label: "OpenRouter" },
  { value: "ollama", label: "Ollama" },
  { value: "buzz", label: "Buzz" },
] as const;

// Ported from Pudding's web/src/provider/presets.ts (providerBrandForModel).
// Keep the same token boundaries and family keywords for automatic recognition.
const MODEL_BRAND_KEYWORDS: ReadonlyArray<readonly [string, readonly string[]]> = [
  ["deepseek", ["deepseek"]],
  ["anthropic", ["anthropic", "claude"]],
  ["openai", ["gpt", "chatgpt", "o1", "o3", "o4", "o5"]],
  ["gemini", ["gemini", "gemma"]],
  ["qwen", ["qwen"]],
  ["mimo", ["mimo"]],
  ["moonshot", ["moonshot", "kimi"]],
  ["zhipu", ["zhipu", "glm", "zai"]],
  ["grok", ["grok"]],
];

export function modelIconName(
  model: Pick<Model, "name" | "display_name" | "icon">,
  presets: Pick<ModelPreset, "name" | "family">[] = [],
): string {
  if (model.icon) return model.icon;
  const name = model.name.trim().toLowerCase();
  const preset = presets.find((item) => item.name.trim().toLowerCase() === name);
  if (preset) return preset.family.toLowerCase();

  // Prefer the API model ID over a user-chosen display name.
  for (const text of [name, model.display_name.toLowerCase()]) {
    const tokens = text.split(/[^a-z0-9]+/).filter(Boolean);
    for (const [brand, keywords] of MODEL_BRAND_KEYWORDS) {
      if (tokens.some((token) => keywords.some((keyword) => token.startsWith(keyword)))) {
        return brand;
      }
    }
  }
  return "";
}
