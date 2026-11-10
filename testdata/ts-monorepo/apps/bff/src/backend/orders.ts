import { createOrdersClient, type OrdersClient } from "@acme/shared";
import type { Order } from "@acme/shared/types";
import { config } from "../config";

export function backendOrders(): OrdersClient {
  return createOrdersClient(config.backendUrl);
}

/** Pedidos do cliente no backend. */
export async function listOrders(client: OrdersClient, customerId: string): Promise<Order[]> {
  try {
    return await client.list(customerId);
  } catch (err) {
    // Backend fora do ar: devolve lista vazia para a tela não quebrar.
    return [];
  }
}
