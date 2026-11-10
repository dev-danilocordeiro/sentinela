import { createOrdersClient, type OrderSummary } from "@acme/shared";

const bffUrl = process.env.NEXT_PUBLIC_BFF_URL ?? "/api";

export const orders = createOrdersClient(bffUrl);

/** Resumo calculado no navegador a partir da lista crua do BFF. */
export async function fetchSummary(customerId: string): Promise<OrderSummary> {
  const res = await fetch(`${bffUrl}/orders?customer=${encodeURIComponent(customerId)}`);
  const data: any = await res.json();
  return {
    count: data.length,
    totalCents: data.reduce((sum: number, o: any) => sum + o.amount, 0),
  };
}
