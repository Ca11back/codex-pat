(() => {
  "use strict";

  const managementBase = "/v0/management/plugins/codex-pat";
  const nativeAuthFilesPath = "/v0/management/auth-files";
  const keyInput = document.getElementById("management-key");
  const patInput = document.getElementById("pat");
  const importForm = document.getElementById("import-form");
  const importButton = document.getElementById("import-button");
  const refreshButton = document.getElementById("refresh-all");
  const rows = document.getElementById("account-rows");
  const count = document.getElementById("account-count");
  const notice = document.getElementById("notice");
  const connectionState = document.getElementById("connection-state");
  let pollTimer = 0;

  function managementKey() {
    return keyInput.value;
  }

  function authHeaders(jsonBody) {
    const key = managementKey();
    if (!key) {
      throw new Error("Management key is required.");
    }
    const headers = { Authorization: `Bearer ${key}` };
    if (jsonBody) {
      headers["Content-Type"] = "application/json";
    }
    return headers;
  }

  async function apiRequest(path, options = {}) {
    const response = await fetch(path, {
      ...options,
      cache: "no-store",
      credentials: "same-origin",
      headers: { ...authHeaders(Boolean(options.body)), ...(options.headers || {}) },
    });
    const contentType = response.headers.get("content-type") || "";
    const payload = contentType.includes("application/json") ? await response.json() : null;
    if (!response.ok) {
      const message = payload && payload.error && payload.error.message
        ? payload.error.message
        : `Request failed (${response.status}).`;
      const error = new Error(message);
      error.status = response.status;
      error.retryable = Boolean(payload && payload.error && payload.error.retryable);
      throw error;
    }
    return payload ? payload.data : null;
  }

  function setNotice(message, kind) {
    if (!message) {
      notice.hidden = true;
      notice.textContent = "";
      notice.className = "notice";
      return;
    }
    notice.hidden = false;
    notice.textContent = message;
    notice.className = `notice notice-${kind}`;
  }

  function setConnectionState(label, state) {
    connectionState.textContent = label;
    connectionState.className = `state state-${state}`;
  }

  function stateLabel(account) {
    switch (account.readiness) {
      case "ready": return "Ready";
      case "pending": return "Pending";
      case "disabled": return "Disabled";
      default: return "Unavailable";
    }
  }

  function formattedTime(value) {
    if (!value) return "Not validated";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "Unavailable";
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(date);
  }

  function actionButton(icon, label, className, handler) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `icon-button ${className || ""}`.trim();
    button.title = label;
    button.setAttribute("aria-label", label);
    const image = document.createElement("img");
    image.src = `/v0/resource/plugins/codex-pat/assets/icons/${icon}.svg`;
    image.alt = "";
    button.appendChild(image);
    button.addEventListener("click", handler);
    return button;
  }

  function textCell(label, value, className) {
    const cell = document.createElement("td");
    cell.dataset.label = label;
    cell.textContent = value;
    if (className) cell.className = className;
    return cell;
  }

  function renderAccounts(accounts) {
    rows.replaceChildren();
    count.textContent = String(accounts.length);
    if (!accounts.length) {
      const row = document.createElement("tr");
      row.className = "empty-row";
      const cell = document.createElement("td");
      cell.colSpan = 5;
      cell.textContent = "No Codex PAT credentials.";
      row.appendChild(cell);
      rows.appendChild(row);
      return;
    }

    for (const account of accounts) {
      const row = document.createElement("tr");
      const accountCell = document.createElement("td");
      accountCell.dataset.label = "Account";
      const primary = document.createElement("span");
      primary.className = "account-primary";
      primary.textContent = account.email || "Email unavailable";
      const idLabel = document.createElement("span");
      idLabel.className = "account-id-label";
      idLabel.textContent = "Account / Workspace ID";
      const idValue = document.createElement("span");
      idValue.className = "account-id-value";
      idValue.textContent = account.account_id || "Unavailable";
      const fileName = document.createElement("span");
      fileName.className = "account-file";
      fileName.textContent = account.name || "Filename unavailable";
      accountCell.append(primary, idLabel, idValue, fileName);
      row.appendChild(accountCell);
      row.appendChild(textCell("Plan", account.plan_type || "Unknown", "cell-muted"));
      row.appendChild(textCell("Validated", formattedTime(account.validated_at), "cell-muted"));

      const stateCell = document.createElement("td");
      stateCell.dataset.label = "State";
      const state = document.createElement("span");
      state.className = `state state-${account.readiness || "unavailable"}`;
      state.textContent = stateLabel(account);
      stateCell.appendChild(state);
      row.appendChild(stateCell);

      const actionsCell = document.createElement("td");
      actionsCell.dataset.label = "Actions";
      const actions = document.createElement("div");
      actions.className = "row-actions";
      actions.appendChild(actionButton("refresh-cw", "Revalidate PAT", "", async (event) => {
        const button = event.currentTarget;
        button.disabled = true;
        setNotice("", "success");
        try {
          await apiRequest(`${managementBase}/revalidate`, {
            method: "POST",
            body: JSON.stringify({ auth_index: account.auth_index }),
          });
          setNotice("Credential revalidated.", "success");
          await loadAccounts({ quiet: true });
        } catch (error) {
          setNotice(error.message, "error");
          await loadAccounts({ quiet: true });
        } finally {
          button.disabled = false;
        }
      }));
      actions.appendChild(actionButton("trash-2", "Delete credential", "danger", async (event) => {
        if (!window.confirm(`Delete ${account.name}?`)) return;
        const button = event.currentTarget;
        button.disabled = true;
        setNotice("", "success");
        try {
          await apiRequest(`${nativeAuthFilesPath}?name=${encodeURIComponent(account.name)}`, {
            method: "DELETE",
          });
          setNotice("Credential deleted.", "success");
          await loadAccounts({ quiet: true });
        } catch (error) {
          setNotice(error.message, "error");
        } finally {
          button.disabled = false;
        }
      }));
      actionsCell.appendChild(actions);
      row.appendChild(actionsCell);
      rows.appendChild(row);
    }
  }

  function schedulePendingPoll(accounts) {
    window.clearTimeout(pollTimer);
    if (!accounts.some((account) => account.readiness === "pending")) return;
    pollTimer = window.setTimeout(() => loadAccounts({ quiet: true }), 2000);
  }

  async function loadAccounts(options = {}) {
    refreshButton.disabled = true;
    if (!options.quiet) setNotice("", "success");
    if (!managementKey()) {
      setConnectionState("Locked", "locked");
      if (!options.quiet) setNotice("Management key is required.", "error");
      refreshButton.disabled = false;
      return;
    }
    try {
      const data = await apiRequest(`${managementBase}/status`);
      const accounts = data && Array.isArray(data.accounts) ? data.accounts : [];
      renderAccounts(accounts);
      setConnectionState("Connected", "ready");
      schedulePendingPoll(accounts);
    } catch (error) {
      setConnectionState(error.status === 401 ? "Locked" : "Unavailable", error.status === 401 ? "locked" : "unavailable");
      if (!options.quiet) setNotice(error.message, "error");
    } finally {
      refreshButton.disabled = false;
    }
  }

  importForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    let pat = patInput.value.trim();
    if (!pat) {
      setNotice("Personal access token is required.", "error");
      return;
    }
    let body = JSON.stringify({ pat });
    pat = "";
    patInput.value = "";
    importButton.disabled = true;
    setNotice("", "success");
    try {
      await apiRequest(`${managementBase}/import`, { method: "POST", body });
      setNotice("Credential imported. Waiting for CPA readiness.", "success");
      await loadAccounts({ quiet: true });
    } catch (error) {
      setNotice(error.message, "error");
    } finally {
      body = "";
      importButton.disabled = false;
      patInput.focus();
    }
  });

  refreshButton.addEventListener("click", () => loadAccounts());
  keyInput.addEventListener("change", () => {
    window.clearTimeout(pollTimer);
    loadAccounts();
  });
})();
