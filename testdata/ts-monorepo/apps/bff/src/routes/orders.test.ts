import { describe, expect, it } from "vitest";
import type { OrdersClient } from "@acme/shared";
import { orderRoutes, summarize } from "./orders";

describe("summarize", () => {
  it("soma só os pedidos não cancelados", () => {
    const got = summarize([
      { status: "open", total: 1000 },
      { status: "paid", total: 250 },
      { status: "cancelled", total: 999 },
    ]);
    expect(got).toEqual({ count: 2, totalCents: 1250 });
  });
});

describe("summary", () => {
  it("resume os pedidos do cliente", async () => {
    const client = {
      list: async () => [{ id: "o-1", customerId: "c-1", status: "open", total: 500, createdAt: "" }],
    } as unknown as OrdersClient;
    let body: unknown;
    const res = { json: (b: unknown) => (body = b) };
    await orderRoutes(client).summary({ query: { customer: "c-1" } } as never, res as never);
    expect(body).toEqual({ count: 1, totalCents: 500 });
  });
});
