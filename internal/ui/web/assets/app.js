// Eco-Agent 本機狀態頁。每 2 秒拉一次 status.json；這同時是給 Agent 的心跳
// （Agent 約 15 秒沒收到就判定頁面已關閉，暫停綁定）。
"use strict";

const POLL_MS = 2000;
const views = ["loading", "binding", "idle", "retry", "bound", "revoked", "offline"];
const pathNames = { computer: "電腦使用", drive: "雲端儲存", printer: "印表機" };
const reasonNames = {
  threshold: "累積達量",
  maxage: "滯留逾時",
  startup: "開機補送",
  shutdown: "關機前",
  manual: "手動",
};

let qrVersion = "";
let expiresAt = null;
let retryAt = null;

function show(name) {
  for (const v of views) {
    document.getElementById("view-" + v).hidden = v !== name;
  }
  document.getElementById("dot").className = "dot " + name;
}

function fmtTime(iso) {
  const d = new Date(iso);
  return d.toLocaleString("zh-TW", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function row(dl, label, value, off) {
  const dt = document.createElement("dt");
  dt.textContent = label;
  const dd = document.createElement("dd");
  dd.textContent = value;
  if (off) dd.className = "off";
  dl.append(dt, dd);
}

function renderBound(s) {
  const dl = document.getElementById("details");
  dl.replaceChildren();
  row(dl, "待上傳", s.queue_count + " 筆");
  if (s.last_upload) {
    const u = s.last_upload;
    const result = u.ok ? "成功" : u.revoked ? "已撤銷" : "未完成，稍後重試";
    row(dl, "上次上傳", fmtTime(u.at) + "・" + result + "（" + (reasonNames[u.reason] || u.reason) + "）");
  } else {
    row(dl, "上次上傳", "尚未上傳", true);
  }
  for (const p of s.paths || []) {
    row(dl, pathNames[p.path] || p.path, p.enabled ? "記錄中" : "未啟用", !p.enabled);
  }
}

function renderBinding(s) {
  const b = s.binding;
  if (!b && s.binding_retry) {
    // 後端連不上或回錯誤：顯示原因與下次重試倒數。
    document.getElementById("retry-message").textContent = s.binding_retry.message;
    retryAt = new Date(s.binding_retry.at);
    tick();
    show("retry");
    return;
  }
  retryAt = null;
  if (!b) {
    show("idle"); // 正在向伺服器索取綁定碼
    return;
  }
  if (b.version !== qrVersion) {
    qrVersion = b.version;
    document.getElementById("qr").src = "qr.png?v=" + encodeURIComponent(b.version);
  }
  expiresAt = new Date(b.expires_at);
  tick();
  show("binding");
}

function tick() {
  const r = document.getElementById("retry-countdown");
  if (retryAt) {
    const left = Math.max(0, Math.round((retryAt - Date.now()) / 1000));
    r.textContent = left > 0 ? left + " 秒後自動重試" : "重試中…";
  } else {
    r.textContent = "";
  }
  const el = document.getElementById("countdown");
  if (!expiresAt) {
    el.textContent = "";
    return;
  }
  const left = Math.max(0, Math.round((expiresAt - Date.now()) / 1000));
  const m = Math.floor(left / 60);
  const sec = String(left % 60).padStart(2, "0");
  el.textContent = left > 0 ? "QR Code 將於 " + m + ":" + sec + " 後更新" : "正在更新 QR Code…";
}

function render(s) {
  if (s.state !== "binding") {
    expiresAt = null;
    retryAt = null;
  }
  switch (s.state) {
    case "binding":
      renderBinding(s);
      break;
    case "bound":
      renderBound(s);
      show("bound");
      break;
    case "revoked": {
      const n = s.discarded_on_revoke;
      const el = document.getElementById("discarded");
      el.hidden = !n;
      el.textContent = n ? "撤銷時有 " + n + " 筆尚未上傳的紀錄已捨棄。" : "";
      show("revoked");
      break;
    }
    default:
      show("idle");
  }
}

async function poll() {
  try {
    const res = await fetch("status.json", { cache: "no-store" });
    if (!res.ok) throw new Error("HTTP " + res.status);
    render(await res.json());
  } catch (_) {
    expiresAt = null;
    show("offline");
  }
}

document.getElementById("retry-now").addEventListener("click", async (ev) => {
  const btn = ev.currentTarget;
  btn.disabled = true;
  try {
    await fetch("retry", { method: "POST", cache: "no-store" });
  } catch (_) {
    // 連不上 Agent 時由下一次 poll 顯示離線畫面。
  }
  await poll();
  btn.disabled = false;
});

poll();
setInterval(poll, POLL_MS);
setInterval(tick, 1000);
// 背景分頁的計時器會被瀏覽器節流；切回分頁時立刻送一次心跳，讓 Agent 馬上恢復綁定。
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) poll();
});
