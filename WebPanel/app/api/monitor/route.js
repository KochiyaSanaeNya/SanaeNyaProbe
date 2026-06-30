import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { NextResponse } from "next/server";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const requestTimeoutMs = 8000;

export async function GET() {
  try {
    const target = await readTarget();
    const response = await fetchTarget(target);
    const body = await response.text();
    const contentType = response.headers.get("content-type") || "application/json; charset=utf-8";

    if (!response.ok) {
      return NextResponse.json(
        { error: `目标接口返回 ${response.status} ${response.statusText}`.trim() },
        { status: 502, headers: noStoreHeaders() },
      );
    }

    return new NextResponse(body, {
      status: 200,
      headers: {
        ...noStoreHeaders(),
        "content-type": contentType,
      },
    });
  } catch (error) {
    return NextResponse.json(
      { error: error instanceof Error ? error.message : "读取监控数据失败" },
      { status: 500, headers: noStoreHeaders() },
    );
  }
}

async function readTarget() {
  const path = join(process.cwd(), "target.conf");
  const content = await readFile(path, "utf8");
  const values = parseConf(content);
  const rawTarget = values.monitor_api || values.target || values.url;

  if (!rawTarget) {
    throw new Error("target.conf 缺少 monitor_api");
  }

  const target = new URL(rawTarget);
  if (target.protocol !== "https:" && target.protocol !== "http:") {
    throw new Error("target.conf 的 monitor_api 必须是 http 或 https URL");
  }

  return target.toString();
}

function parseConf(content) {
  const values = {};

  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) {
      continue;
    }

    const separator = line.indexOf("=");
    if (separator === -1) {
      if (!values.monitor_api) {
        values.monitor_api = line;
      }
      continue;
    }

    const key = line.slice(0, separator).trim();
    const value = line.slice(separator + 1).trim();
    if (key) {
      values[key] = stripQuotes(value);
    }
  }

  return values;
}

function stripQuotes(value) {
  if (
    (value.startsWith("\"") && value.endsWith("\"")) ||
    (value.startsWith("'") && value.endsWith("'"))
  ) {
    return value.slice(1, -1);
  }
  return value;
}

async function fetchTarget(target) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), requestTimeoutMs);

  try {
    return await fetch(target, {
      method: "GET",
      headers: { Accept: "application/json" },
      cache: "no-store",
      signal: controller.signal,
    });
  } catch (error) {
    if (error instanceof Error && error.name === "AbortError") {
      throw new Error("目标接口请求超时");
    }
    throw error;
  } finally {
    clearTimeout(timer);
  }
}

function noStoreHeaders() {
  return {
    "cache-control": "no-store",
  };
}
