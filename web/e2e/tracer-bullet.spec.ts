import { expect, test } from "@playwright/test";

test("the native control plane serves the dashboard and versioned API", async ({
  page,
  request,
}) => {
  const healthResponse = await request.get("/api/v1/health");
  expect(healthResponse.ok()).toBe(true);
  await expect(healthResponse.json()).resolves.toEqual({ status: "ok" });

  await page.goto("/");
  await expect(
    page.getByRole("heading", {
      name: "Know your recovery works before the incident.",
    }),
  ).toBeVisible();
  await expect(page.getByText("Connected to Rehearse dev")).toBeVisible();
});
