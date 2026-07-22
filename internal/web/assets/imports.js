(() => {
  "use strict";

  const concurrency = 2;

  function statusNode(sourceID) {
    for (const node of document.querySelectorAll("[data-import-status]")) {
      if (node.dataset.importStatus === sourceID) return node;
    }
    return null;
  }

  function setStatus(sourceID, message, state) {
    const node = statusNode(sourceID);
    if (!node) return;
    node.textContent = message;
    node.dataset.state = state;
  }

  function progressMessage(snapshot, joined) {
    const prefix = joined ? "Joined import" : snapshot.phase;
    return `${prefix}: ${snapshot.records_processed} records, ${snapshot.events_processed} events`;
  }

  function terminalMessage(snapshot, eventName, joined) {
    const diagnostics = (snapshot.recent_diagnostics || [])
      .map((diagnostic) => `${diagnostic.severity} ${diagnostic.code}: ${diagnostic.message}`)
      .join("; ");
    const omitted = snapshot.diagnostics_omitted > 0 ? `; ${snapshot.diagnostics_omitted} earlier diagnostics omitted` : "";
    const diagnosticText = diagnostics ? ` — ${diagnostics}${omitted}` : omitted;
    if (eventName === "failure") {
      return `Failed: ${snapshot.failure || "import failed"}${diagnosticText}`;
    }
    const result = snapshot.diagnostics_observed > 0 ? "Completed with diagnostics" : "Completed";
    const joinedText = joined ? " (joined existing import)" : "";
    return `${result}${joinedText}: ${snapshot.import_results_observed} sessions, ${snapshot.records_committed} records committed${diagnosticText}`;
  }

  function consumeEvents(buffer, onEvent) {
    let boundary;
    while ((boundary = buffer.indexOf("\n\n")) !== -1) {
      const block = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      let eventName = "message";
      const data = [];
      for (const line of block.split("\n")) {
        if (line.startsWith("event:")) eventName = line.slice(6).trim();
        if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
      }
      if (data.length > 0) onEvent(eventName, JSON.parse(data.join("\n")));
    }
    return buffer;
  }

  async function importSource(sourceID) {
    setStatus(sourceID, "Starting…", "running");
    try {
      const response = await fetch("/imports", {
        method: "POST",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8",
          "X-AgentSession-Request": "import",
        },
        body: new URLSearchParams({ source: sourceID }),
      });
      if (!response.ok || !response.body) {
        setStatus(sourceID, `Import unavailable (${response.status})`, "failed");
        return;
      }
      const joined = response.headers.get("X-AgentSession-Import-Joined") === "true";
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      let terminal = false;
      for (;;) {
        const chunk = await reader.read();
        buffer += decoder.decode(chunk.value || new Uint8Array(), { stream: !chunk.done }).replaceAll("\r\n", "\n");
        buffer = consumeEvents(buffer, (eventName, snapshot) => {
          if (eventName === "completion" || eventName === "failure") {
            terminal = true;
            setStatus(sourceID, terminalMessage(snapshot, eventName, joined), eventName === "failure" ? "failed" : snapshot.diagnostics_observed > 0 ? "partial" : "completed");
          } else {
            setStatus(sourceID, progressMessage(snapshot, joined), "running");
          }
        });
        if (chunk.done) break;
      }
      if (!terminal) setStatus(sourceID, "Import stream ended before a terminal result", "failed");
    } catch (error) {
      setStatus(sourceID, `Import connection failed: ${error instanceof Error ? error.message : "unknown error"}`, "failed");
    }
  }

  async function runQueue(sourceIDs) {
    let next = 0;
    async function worker() {
      while (next < sourceIDs.length) {
        const sourceID = sourceIDs[next++];
        await importSource(sourceID);
      }
    }
    await Promise.all(Array.from({ length: Math.min(concurrency, sourceIDs.length) }, worker));
    if (window.htmx) window.htmx.ajax("GET", "/fragments/sessions", { target: "#sessions-panel", swap: "innerHTML" });
  }

  document.addEventListener("submit", (event) => {
    if (!(event.target instanceof HTMLFormElement) || event.target.id !== "import-form") return;
    event.preventDefault();
    const sourceIDs = Array.from(event.target.querySelectorAll('input[name="source"]:checked'), (input) => input.value);
    if (sourceIDs.length === 0) return;
    for (const input of event.target.querySelectorAll('input[name="source"]')) input.disabled = true;
    const submit = event.target.querySelector('button[type="submit"]');
    if (submit) submit.disabled = true;
    runQueue(sourceIDs).finally(() => {
      for (const input of event.target.querySelectorAll('input[name="source"]')) input.disabled = false;
      if (submit) submit.disabled = false;
    });
  });
})();
