/**
 * BeaconGate Apps Script forwarder (v1.1).
 *
 * This script is the "smart base64 boundary" between the BeaconGate
 * client and the operator's BeaconGate server. The client posts a
 * base64-text body to this script's /exec URL; the script decodes it
 * back to binary, forwards it to the operator's VPS as binary, and
 * encodes the binary response back to base64 text.
 *
 * The BeaconGate VPS server stays binary-only — this script is the
 * ONLY place that knows about base64.
 *
 * SETUP:
 *   1. Create a new Apps Script project at https://script.google.com/.
 *   2. Paste this entire file as Code.gs.
 *   3. Edit RELAY_URL below to point at your VPS BeaconGate server.
 *   4. Deploy → New deployment → type "Web app" → Execute as "Me",
 *      Who has access "Anyone" → Deploy.
 *   5. Copy the Deployment ID (NOT the Script ID) into your
 *      client_config.json under transport.options.script_keys.
 *   6. Every time you edit this file, you MUST create a new deployment
 *      and update script_keys; saving alone is not enough.
 *
 * QUOTAS:
 *   - Apps Script allows ~20,000 UrlFetchApp invocations per Google
 *     account per day (resets at midnight Pacific).
 *   - Each doPost or doGet here counts as one invocation.
 *   - Configure multiple script_keys (across multiple Google accounts)
 *     in your client config to extend the daily ceiling.
 */

// EDIT THIS LINE: full URL of your BeaconGate VPS server's tunnel endpoint.
const RELAY_URL = 'http://YOUR.VPS.IP:8080/tunnel';

const VERSION = 1;
const PROTOCOL = 1;

/**
 * doPost: receive a base64-text encrypted batch from the BeaconGate
 * client, forward as binary to the VPS, return the binary response
 * encoded back to base64 text.
 *
 * The client sends Content-Type: text/plain because Apps Script's
 * e.postData.contents is a JS string (text-oriented). We base64-decode
 * it ourselves so the BeaconGate VPS receives raw sealed bytes,
 * identical to what direct https-mode clients deliver.
 */
function doPost(e) {
  bumpDailyCount_();

  if (!e || !e.postData || !e.postData.contents) {
    return ContentService
      .createTextOutput('')
      .setMimeType(ContentService.MimeType.TEXT);
  }

  let decoded;
  try {
    decoded = Utilities.base64Decode(e.postData.contents);
  } catch (err) {
    return ContentService
      .createTextOutput('')
      .setMimeType(ContentService.MimeType.TEXT);
  }

  const options = {
    method: 'post',
    payload: decoded,
    contentType: 'application/octet-stream',
    muteHttpExceptions: true,
    followRedirects: false
  };

  let response;
  try {
    response = UrlFetchApp.fetch(RELAY_URL, options);
  } catch (err) {
    return ContentService
      .createTextOutput('')
      .setMimeType(ContentService.MimeType.TEXT);
  }

  // Forward through whatever status the VPS returned, but always
  // base64-encode the body for the return trip.
  const bodyBytes = response.getContent();
  const encoded = Utilities.base64Encode(bodyBytes);
  return ContentService
    .createTextOutput(encoded)
    .setMimeType(ContentService.MimeType.TEXT);
}

/**
 * doGet: report the deployment's self-tracked daily invocation count
 * back to the client. The BeaconGate appsscript transport polls this
 * every 30 minutes per deployment to surface a live quota number in
 * its diagnostics output.
 *
 * Counter is keyed by date (Pacific) and increments on every doPost
 * and doGet. Stale date keys are purged on the first request of a
 * new day so PropertiesService doesn't grow unbounded.
 */
function doGet(e) {
  bumpDailyCount_();
  const today = todayPacificISO_();
  const props = PropertiesService.getScriptProperties();
  const countStr = props.getProperty('count_' + today);
  const count = countStr ? parseInt(countStr, 10) || 0 : 0;
  const payload = {
    ok: true,
    date: today,
    count: count,
    version: VERSION,
    protocol: PROTOCOL
  };
  return ContentService
    .createTextOutput(JSON.stringify(payload))
    .setMimeType(ContentService.MimeType.JSON);
}

/**
 * bumpDailyCount_ increments the per-day invocation counter. On the
 * first invocation of a new day (Pacific), it also purges stale date
 * keys to keep PropertiesService bounded.
 */
function bumpDailyCount_() {
  const props = PropertiesService.getScriptProperties();
  const today = todayPacificISO_();
  const key = 'count_' + today;
  const current = props.getProperty(key);
  const next = (current ? parseInt(current, 10) || 0 : 0) + 1;
  props.setProperty(key, String(next));

  // Purge stale date keys lazily — only once per first-of-day call.
  if (!current) {
    purgeOldCountKeys_(props, today);
  }
}

function purgeOldCountKeys_(props, today) {
  const all = props.getProperties();
  for (const k in all) {
    if (k.indexOf('count_') === 0 && k !== ('count_' + today)) {
      props.deleteProperty(k);
    }
  }
}

/**
 * todayPacificISO_ returns the current date in America/Los_Angeles
 * as YYYY-MM-DD. Apps Script's Utilities.formatDate honors the
 * Pacific timezone, matching where Google resets the daily UrlFetch
 * quota.
 */
function todayPacificISO_() {
  return Utilities.formatDate(new Date(), 'America/Los_Angeles', 'yyyy-MM-dd');
}
