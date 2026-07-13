import { useEffect, useState } from "react";

type Connection =
  | { state: "connecting" }
  | { state: "connected"; version: string }
  | { state: "error" };

type VersionResponse = {
  version: string;
};

export function App() {
  const [connection, setConnection] = useState<Connection>({
    state: "connecting",
  });

  useEffect(() => {
    const controller = new AbortController();

    async function connect() {
      try {
        const response = await fetch("/api/v1/version", {
          headers: { Accept: "application/json" },
          signal: controller.signal,
        });

        if (!response.ok) {
          throw new Error(`version request failed with ${response.status}`);
        }

        const payload = (await response.json()) as VersionResponse;
        setConnection({ state: "connected", version: payload.version });
      } catch {
        if (!controller.signal.aborted) {
          setConnection({ state: "error" });
        }
      }
    }

    void connect();
    return () => controller.abort();
  }, []);

  return (
    <main>
      <header className="masthead">
        <a className="wordmark" href="/" aria-label="Rehearse home">
          REHEARSE
        </a>
        <span className="phase">Foundation build</span>
      </header>

      <section className="hero" aria-labelledby="hero-title">
        <p className="eyebrow">Recovery evidence, before the incident</p>
        <h1 id="hero-title">Know your recovery works before the incident.</h1>
        <p className="summary">
          Rehearse restores backups into disposable environments, verifies the
          application, records what happened, and cleans up.
        </p>

        <div className={`connection connection--${connection.state}`} role="status">
          <span className="connection__signal" aria-hidden="true" />
          {connection.state === "connecting" && "Connecting to the local control plane"}
          {connection.state === "connected" &&
            `Connected to Rehearse ${connection.version}`}
          {connection.state === "error" &&
            "Unable to reach the local control plane"}
        </div>
      </section>

      <ol className="recovery-path" aria-label="Recovery drill stages">
        <li>Acquire backup</li>
        <li>Restore state</li>
        <li>Boot sandbox</li>
        <li>Run probes</li>
        <li>Record evidence</li>
        <li>Clean up</li>
      </ol>
    </main>
  );
}
