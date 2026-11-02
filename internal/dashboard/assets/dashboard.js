"use strict";

(() => {
  const basePath = window.location.pathname;
  const $ = (id) => document.getElementById(id);
  const list = $("devices");
  const dialog = $("preview-dialog");
  const state = { devices: [], cursors: [""], page: 0, next: "", loaded: false, busy: false, timer: null, interval: 30, expired: false };
  let previewOwner = "";

  // All API text enters the DOM as text, including resource names and reasons.
  function node(tag, className, text) {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (text !== undefined) el.textContent = text;
    return el;
  }

  function dateValue(value) {
    const ms = typeof value === "string" ? Date.parse(value) : NaN;
    return Number.isFinite(ms) ? ms : null;
  }

  function age(value) {
    const ms = dateValue(value);
    if (ms === null) return "—";
    const seconds = Math.floor((Date.now() - ms) / 1000);
    if (seconds < 0) return "in the future";
    if (seconds < 60) return "just now";
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ${Math.floor(seconds % 3600 / 60)}m ago`;
    return `${Math.floor(seconds / 86400)}d ${Math.floor(seconds % 86400 / 3600)}h ago`;
  }

  const dateFormat = new Intl.DateTimeFormat("en", { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
  const fullDateFormat = new Intl.DateTimeFormat("en", { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false, timeZoneName: "long" });
  function updateTime(el) {
    const ms = dateValue(el.dataset.time);
    el.title = ms === null ? "Unknown time" : fullDateFormat.format(ms);
    el.textContent = ms === null ? `${el.dataset.label}—` : `${el.dataset.label}${dateFormat.format(ms)} · ${age(el.dataset.time)}`;
  }

  function timestamp(value, label = "") {
    const el = node("time");
    el.dataset.time = value || "";
    el.dataset.label = label;
    if (dateValue(value) !== null) el.dateTime = value;
    updateTime(el);
    return el;
  }

  function field(label, value) {
    const el = node("div");
    el.append(node("dt", "", label), node("dd", "", value ?? "—"));
    return el;
  }

  const scopes = { "recorded-delivery": "Recorded delivery", "device-report": "Device report", device: "Device", screen: "Screen", communication: "Communication", assignment: "Assignment", record: "Delivery record" };
  function signal(label, value) {
    const stateName = value?.state || "Unknown";
    const tone = stateName === "Ready" ? "good" : ["Silent", "Render failed"].includes(stateName) ? "bad" : ["Serving stale image", "Image pending"].includes(stateName) ? "warning" : "";
    const el = node("div", "signal");
    const detail = node("dd");
    detail.append(node("span", `badge ${tone}`, stateName));
    if (label === "Rendering") detail.append(node("span", "signal-scope", scopes[value?.scope] || "Unknown scope"));
    el.append(node("dt", "", label), detail);
    return el;
  }

  function previewURL(device) {
    const image = device.image;
    if (!image || image.previewState !== "available" || !/^[a-f0-9]{64}$/.test(image.digest)) return "";
    const expected = `${basePath}api/devices/${encodeURIComponent(device.name)}/preview/${image.digest}.png`;
    return image.previewURL === expected ? expected : "";
  }

  const observer = "IntersectionObserver" in window ? new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (entry.isIntersecting) {
        const img = entry.target;
        observer.unobserve(img);
        img.src = img.dataset.src;
      }
    }
  }, { rootMargin: "200px 0px" }) : null;

  function imageError(container, retry) {
    const message = node("div", "preview-message", "Preview unavailable");
    const hadFocus = container.contains(document.activeElement);
    const button = node("button", "", "Retry preview");
    button.type = "button";
    button.dataset.focusKey = "preview-retry";
    button.addEventListener("click", retry);
    message.append(node("br"), button);
    container.replaceChildren(message);
    if (hadFocus) button.focus({ preventScroll: true });
  }

  function preview(device) {
    const frame = node("div", "preview-frame");
    const url = previewURL(device);
    frame.dataset.source = url;
    if (!url) {
      frame.append(node("p", "preview-message", device.image || device.recordStatus === "unavailable" ? "Preview unavailable" : "No image available"));
      return frame;
    }
    const load = () => {
      const hadFocus = frame.contains(document.activeElement);
      const button = node("button", "preview-button");
      button.type = "button";
      button.dataset.expand = device.name;
      button.dataset.focusKey = "preview";
      button.setAttribute("aria-label", `Enlarge recorded image for ${device.name}`);
      const img = node("img");
      img.alt = `Recorded delivery for ${device.name}, screen ${device.image.screenName}`;
      img.loading = "lazy";
      img.decoding = "async";
      img.dataset.src = url;
      img.addEventListener("error", () => imageError(frame, load), { once: true });
      button.append(img, node("span", "zoom", "Enlarge ↗"));
      frame.replaceChildren(button);
      if (hadFocus) button.focus({ preventScroll: true });
      if (observer) observer.observe(img);
      else img.src = url;
    };
    // Observation starts after insertion into the document.
    frame.loadPreview = load;
    return frame;
  }

  function assignment(value) {
    const el = node("div", "assignment");
    el.append(node("p", "label", "Assignment"));
    const title = node("div", "assignment-title");
    const mode = { single: "Single screen", playlist: "Playlist", automatic: "Automatic selection" }[value?.mode] || "Unknown";
    title.append(node("span", "assignment-name", value?.name || mode));
    if (value?.name) title.append(node("span", "assignment-mode", mode));
    el.append(title);
    if (value?.mode === "playlist" && value.failurePolicy) el.append(node("p", "label", `On failure: ${value.failurePolicy}`));
    if (value?.mode !== "single") {
      const details = node("details");
      details.dataset.focusKey = "screens";
      const screens = value?.screens || [];
      const summary = node("summary", "", `${screens.length} ${screens.length === 1 ? "screen" : "screens"}`);
      summary.dataset.focusKey = "screen-summary";
      details.append(summary);
      const members = node("ol", "screen-list");
      for (const screen of screens) members.append(node("li", screen.available ? "" : "unavailable", `${screen.name}${screen.available ? "" : " · Unavailable"}`));
      details.append(members);
      el.append(details);
    } else if (value.screens?.some((screen) => !screen.available)) {
      el.append(node("p", "label unavailable", "Screen unavailable"));
    }
    return el;
  }

  function deviceRow(device) {
    const row = node("article", "device");
    row.dataset.name = device.name;
    row.dataset.signature = JSON.stringify(device);
    row.setAttribute("aria-label", `${device.namespace} / ${device.name}`);
    const heading = node("div", "device-heading");
    heading.append(node("h2", "", device.name), node("span", "namespace", device.namespace));
    row.append(heading);
    const body = node("div", "device-body");
    const imageBlock = node("div");
    imageBlock.append(preview(device));
    const caption = node("div", "image-caption");
    caption.append(node("p", "label", "Last recorded delivery"));
    if (device.image) {
      caption.append(node("p", "screen-name", device.image.screenName || "—"));
      if (device.image.result === "stale") caption.append(node("span", "record-tag", "Recorded stale image"));
      const times = node("div", "image-times");
      times.append(timestamp(device.image.generatedAt, "Generated "), timestamp(device.image.issuedAt, "Issued "));
      caption.append(times);
    } else if (device.recordStatus === "unavailable") {
      caption.append(node("p", "label", "Delivery record unavailable"));
    }
    imageBlock.append(caption);
    const activity = node("div");
    const signals = node("dl", "signals");
    signals.append(signal("Communication", device.communication), signal("Delivery", device.delivery), signal("Rendering", device.rendering));
    activity.append(signals, assignment(device.assignment));
    const hardware = node("div", "hardware");
    const telemetry = node("dl", "telemetry");
    const battery = [];
    if (Number.isFinite(device.battery?.percent)) battery.push(`${device.battery.percent}%`);
    if (Number.isFinite(device.battery?.voltage)) battery.push(`${device.battery.voltage.toFixed(2)} V`);
    telemetry.append(field("Battery", battery.length ? battery.join(" · ") : "—"), field("Wi-Fi", Number.isFinite(device.network?.rssi) ? `${device.network.rssi} dBm` : "—"));
    const info = node("dl", "device-info");
    const display = device.display;
    info.append(field("Model", device.model || "—"), field("Firmware", device.firmware || "—"), field("Display", display ? `${display.width} × ${display.height} · ${display.palette} · ${display.rotation}°` : "—"));
    const seen = field("Last seen", "");
    seen.querySelector("dd").append(timestamp(device.lastSeenAt));
    info.append(seen);
    hardware.append(telemetry, info);
    body.append(imageBlock, activity, hardware);
    row.append(body);
    if (device.problems?.length) {
      const problems = node("ul", "problems");
      problems.setAttribute("aria-label", "Problems");
      for (const problem of device.problems) {
        const item = node("li");
        item.append(node("span", "problem-context", `${scopes[problem.scope] || problem.scope}${problem.screen ? ` / ${problem.screen}` : ""}: `), document.createTextNode(problem.message));
        problems.append(item);
      }
      row.append(problems);
    }
    return row;
  }

  function forgetImages(root) {
    if (observer) root.querySelectorAll("img").forEach((img) => observer.unobserve(img));
  }

  function render(devices, changedPage) {
    const existing = new Map(Array.from(list.querySelectorAll(".device"), (row) => [row.dataset.name, row]));
    const focus = document.activeElement;
    const focusName = focus?.closest(".device")?.dataset.name;
    const focusKey = focus?.dataset.focusKey;
    const rows = [];
    for (const device of devices) {
      const previous = existing.get(device.name);
      existing.delete(device.name);
      if (!changedPage && previous?.dataset.signature === JSON.stringify(device)) {
        rows.push(previous);
        continue;
      }
      const row = deviceRow(device);
      const frame = row.querySelector(".preview-frame");
      const oldFrame = previous?.querySelector(".preview-frame");
      // Keep already loaded images across telemetry/status refreshes. no-store
      // previews should not be fetched again just because another field changed.
      if (!changedPage && frame.dataset.source && frame.dataset.source === oldFrame?.dataset.source) frame.replaceWith(oldFrame);
      else frame.loadPreview?.();
      if (!changedPage && previous?.querySelector("details")?.open && row.querySelector("details")) row.querySelector("details").open = true;
      if (previous) forgetImages(previous);
      rows.push(row);
    }
    existing.forEach(forgetImages);
    // Remove obsolete rows first so inserting a changed row does not move
    // unrelated focused controls or discard already loaded images.
    const retained = new Set(rows);
    Array.from(list.children).forEach((child) => { if (!retained.has(child)) child.remove(); });
    rows.forEach((row, index) => {
      if (list.children[index] !== row) list.insertBefore(row, list.children[index] || null);
    });
    if (!devices.length) list.append(node("p", "empty-state", state.next ? "No devices on this page. Continue to the next page." : state.page === 0 ? "No devices found" : "No devices on this page"));
    if (!changedPage && focusName && focusKey && document.activeElement !== focus && !dialog.open) {
      const row = rows.find((item) => item.dataset.name === focusName);
      const target = Array.from(row?.querySelectorAll("[data-focus-key]") || []).find((item) => item.dataset.focusKey === focusKey);
      (target || $("page-title")).focus({ preventScroll: true });
    }
  }

  function navigation() {
    $("previous").disabled = state.busy || state.page === 0;
    $("next").disabled = state.busy || !state.next || state.expired;
    $("pagination").hidden = !state.loaded || (state.page === 0 && !state.next);
    $("page-number").textContent = `Page ${state.page + 1}`;
  }

  function showFailure(expired) {
    const notice = $("notice");
    notice.replaceChildren(node("span", "", expired ? "This page has expired. Return to the first page to refresh the list." : state.loaded ? "Could not update devices. Showing the last successful update." : "Could not load devices. Check the connection and try again."));
    const retry = node("button", "", expired ? "First page" : "Try again");
    retry.type = "button";
    retry.addEventListener("click", () => loadPage(expired ? 0 : state.page, true));
    notice.append(retry);
    notice.hidden = false;
  }

  async function loadPage(page, userAction = false) {
    if (state.busy) return;
    state.busy = true;
    clearTimeout(state.timer);
    navigation();
    list.setAttribute("aria-busy", "true");
    const controller = new AbortController();
    const deadline = setTimeout(() => controller.abort(), 15000);
    try {
      const query = new URLSearchParams({ limit: "20" });
      if (state.cursors[page]) query.set("continue", state.cursors[page]);
      const response = await fetch(`${basePath}api/devices?${query}`, { signal: controller.signal, cache: "no-store", credentials: "same-origin" });
      if (!response.ok) {
        const error = new Error("Device page unavailable");
        error.expired = response.status === 410;
        throw error;
      }
      const data = await response.json();
      if (!Array.isArray(data.devices) || data.devices.length > 20 || !data.devices.every((device) => typeof device.name === "string" && typeof device.namespace === "string") || (data.continue !== undefined && typeof data.continue !== "string")) throw new Error("Invalid device page");
      const changedPage = page !== state.page;
      state.page = page;
      state.next = data.continue || "";
      state.cursors[page + 1] = state.next;
      state.cursors.length = page + 2;
      state.devices = data.devices;
      state.expired = false;
      render(data.devices, changedPage);
      state.loaded = true;
      // UI polling is independent of the device's configured poll interval.
      // pollSeconds belongs to the backend's communication-silence threshold.
      $("updated").replaceChildren(timestamp(data.observedAt, "Updated "));
      $("page-count").textContent = `${data.devices.length} on this page`;
      $("page-count").hidden = false;
      $("notice").hidden = true;
      if (userAction) {
        $("announcement").textContent = `Page ${page + 1}, ${data.devices.length} devices loaded.`;
        if (changedPage || $("notice").contains(document.activeElement) || !document.activeElement?.isConnected) $("page-title").focus();
      }
    } catch (error) {
      state.expired = Boolean(error.expired);
      showFailure(state.expired);
    } finally {
      clearTimeout(deadline);
      state.busy = false;
      $("loading").hidden = true;
      list.setAttribute("aria-busy", "false");
      navigation();
      if (!state.expired && !document.hidden) state.timer = setTimeout(() => loadPage(state.page), state.interval * 1000);
    }
  }

  function openPreview(device) {
    const url = previewURL(device);
    if (!url) return;
    previewOwner = device.name;
    $("preview-title").textContent = `${device.name} / ${device.image.screenName}`;
    const content = $("preview-content");
    const frame = node("div", "dialog-image");
    const load = () => {
      const hadFocus = frame.contains(document.activeElement);
      const img = node("img");
      img.alt = `Recorded delivery for ${device.name}, screen ${device.image.screenName}`;
      img.addEventListener("error", () => imageError(frame, load), { once: true });
      img.src = url;
      frame.replaceChildren(img);
      if (hadFocus) $("close-preview").focus({ preventScroll: true });
    };
    load();
    const caption = node("div", "dialog-caption");
    caption.append(node("p", "label", "Last recorded delivery"), timestamp(device.image.generatedAt, "Generated "), node("br"), timestamp(device.image.issuedAt, "Issued "));
    content.replaceChildren(frame, caption);
    dialog.showModal();
    $("close-preview").focus();
  }

  list.addEventListener("click", (event) => {
    const button = event.target.closest("[data-expand]");
    const device = state.devices.find((item) => item.name === button?.dataset.expand);
    if (device) openPreview(device);
  });
  $("close-preview").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => {
    const row = Array.from(list.querySelectorAll(".device")).find((item) => item.dataset.name === previewOwner);
    (row?.querySelector("[data-expand], .preview-message button") || $("page-title")).focus({ preventScroll: true });
    $("preview-content").replaceChildren();
  });
  $("previous").addEventListener("click", () => loadPage(state.page - 1, true));
  $("next").addEventListener("click", () => loadPage(state.page + 1, true));
  document.addEventListener("visibilitychange", () => {
    clearTimeout(state.timer);
    if (!document.hidden && !state.expired) loadPage(state.page);
  });
  setInterval(() => document.querySelectorAll("time[data-time]").forEach(updateTime), 10000);
  loadPage(0);
})();
