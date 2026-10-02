import "@testing-library/jest-dom/vitest";

import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// @testing-library/react's auto-cleanup relies on detecting a global
// afterEach, which Vitest does not provide unless test.globals is enabled.
// Register it explicitly so each test unmounts the previous render.
afterEach(() => {
  cleanup();
});
