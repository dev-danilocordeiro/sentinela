import type { Order } from "./types";

export type Fetcher = (input: string, init?: RequestInit) => Promise<Response>;

export class HttpError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

/** Cliente HTTP dos pedidos, usado pelo BFF (contra o backend) e pelo web (contra o BFF). */
export function createOrdersClient(baseUrl: string, fetcher: Fetcher = fetch) {
  async function request<T>(path: string, init?: RequestInit): Promise<T> {
    const res = await fetcher(baseUrl + path, init);
    if (!res.ok) {
      throw new HttpError(res.status, `${init?.method ?? "GET"} ${path}: ${res.status}`);
    }
    return (res.status === 204 ? undefined : await res.json()) as T;
  }
  return {
    list: (customerId: string) => request<Order[]>(`/orders?customer=${encodeURIComponent(customerId)}`),
    get: (id: string) => request<Order>(`/orders/${encodeURIComponent(id)}`),
    cancel: (id: string) => request<void>(`/orders/${encodeURIComponent(id)}/cancel`, { method: "POST" }),
  };
}

export type OrdersClient = ReturnType<typeof createOrdersClient>;
