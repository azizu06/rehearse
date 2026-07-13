import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { App } from "./App";

describe("App", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the control-plane version after connecting", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ version: "test" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    render(<App />);

    expect(
      screen.getByRole("heading", {
        name: "Know your recovery works before the incident.",
      }),
    ).toBeVisible();
    expect(await screen.findByText("Connected to Rehearse test")).toBeVisible();
  });
});
