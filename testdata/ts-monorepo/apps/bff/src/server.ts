import express from "express";
import { backendOrders } from "./backend/orders";
import { config } from "./config";
import { orderRoutes } from "./routes/orders";

const app = express();
const orders = orderRoutes(backendOrders());

app.get("/api/orders/summary", orders.summary);
app.post("/api/orders/:id/cancel", orders.cancel);

app.listen(config.port, () => {
  console.log(`bff na porta ${config.port}`);
});
