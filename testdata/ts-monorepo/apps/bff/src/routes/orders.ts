import type { Request, Response } from "express";
import type { OrderSummary, OrdersClient } from "@acme/shared";
import { listOrders } from "../backend/orders";

export function summarize(orders: { status: string; total: number }[]): OrderSummary {
  const open = orders.filter((o) => o.status !== "cancelled");
  return { count: open.length, totalCents: open.reduce((sum, o) => sum + o.total, 0) };
}

export function orderRoutes(client: OrdersClient) {
  return {
    async summary(req: Request, res: Response) {
      const orders = await listOrders(client, String(req.query.customer ?? ""));
      res.json(summarize(orders));
    },

    async cancel(req: Request, res: Response) {
      client.cancel(String(req.params.id));
      res.status(204).end();
    },
  };
}
