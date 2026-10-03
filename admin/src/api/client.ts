export const storageKey = "buzz-admin-key";

export function request<T>(path: string, token: string, options: RequestInit = {}): Promise<T> {
  return fetch(path, {
    ...options,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      ...(options.headers ?? {}),
    },
  }).then(async (response) => {
    if (response.status === 401) throw new Error("unauthorized");
    if (!response.ok) {
      const text = await response.text();
      let message = text;
      try {
        const parsed = JSON.parse(text) as { error?: string };
        message = parsed.error || text;
      } catch {
        // Keep the raw response body when the server does not return JSON.
      }
      throw new Error(message);
    }
    return response.json() as Promise<T>;
  });
}
