#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const rootDir = path.resolve(__dirname, "..");
const pluginsDir = process.argv[2] ? path.resolve(process.argv[2]) : path.join(rootDir, "plugins");
const schemaPath = path.join(pluginsDir, "schema.json");

const ID_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const SEMVER_RE = /^\d+\.\d+\.\d+$/;
const CATEGORIES = new Set(["latency", "journal", "alerts", "host", "container"]);
const SEVERITIES = new Set(["info", "warn", "critical"]);

function validateManifest(data, schema) {
  const errors = [];
  if (!data || typeof data !== "object" || Array.isArray(data)) {
    return ["manifest root must be a JSON object"];
  }

  const allowedKeys = new Set(Object.keys(schema.properties || {}));
  for (const key of Object.keys(data)) {
    if (!allowedKeys.has(key)) {
      errors.push(`unexpected property "${key}"`);
    }
  }
  for (const req of schema.required || []) {
    if (!(req in data)) {
      errors.push(`missing required field "${req}"`);
    }
  }

  if (typeof data.id !== "string" || !ID_RE.test(data.id)) {
    errors.push("id must match kebab-case pattern");
  }
  if (typeof data.name !== "string" || data.name.trim().length < 3) {
    errors.push("name must be a string with at least 3 characters");
  }
  if (typeof data.version !== "string" || !SEMVER_RE.test(data.version)) {
    errors.push("version must be semver X.Y.Z");
  }
  if (typeof data.category !== "string" || !CATEGORIES.has(data.category)) {
    errors.push(`category must be one of: ${[...CATEGORIES].join(", ")}`);
  }
  if (typeof data.author !== "string" || data.author.trim().length < 2) {
    errors.push("author must be a non-empty string");
  }
  if (typeof data.description !== "string" || data.description.trim().length < 12) {
    errors.push("description must be at least 12 characters");
  }
  if (typeof data.min_tradebox_version !== "string" || !SEMVER_RE.test(data.min_tradebox_version)) {
    errors.push("min_tradebox_version must be semver X.Y.Z");
  }
  if (typeof data.enabled_by_default !== "boolean") {
    errors.push("enabled_by_default must be a boolean");
  }
  if (!Array.isArray(data.tags) || data.tags.length === 0 || data.tags.some((t) => typeof t !== "string" || t.length < 2)) {
    errors.push("tags must be a non-empty array of strings");
  }
  if (!Array.isArray(data.triggers) || data.triggers.length === 0 || data.triggers.some((t) => typeof t !== "string" || t.length < 3)) {
    errors.push("triggers must be a non-empty array of strings");
  }
  if (!data.config || typeof data.config !== "object" || Array.isArray(data.config)) {
    errors.push("config must be a JSON object");
  }
  if (!Array.isArray(data.rules) || data.rules.length === 0) {
    errors.push("rules must be a non-empty array");
  } else {
    data.rules.forEach((rule, idx) => {
      if (!rule || typeof rule !== "object" || Array.isArray(rule)) {
        errors.push(`rules[${idx}] must be an object`);
        return;
      }
      if (typeof rule.id !== "string" || !ID_RE.test(rule.id)) {
        errors.push(`rules[${idx}].id must be kebab-case`);
      }
      if (typeof rule.match !== "string" || rule.match.trim().length < 2) {
        errors.push(`rules[${idx}].match must be a non-empty string`);
      }
      if (typeof rule.severity !== "string" || !SEVERITIES.has(rule.severity)) {
        errors.push(`rules[${idx}].severity must be info, warn, or critical`);
      }
      if (typeof rule.action !== "string" || rule.action.trim().length < 3) {
        errors.push(`rules[${idx}].action must be a non-empty string`);
      }
    });
  }

  return errors;
}

function main() {
  if (!fs.existsSync(schemaPath)) {
    console.error(`Error: schema file missing at ${schemaPath}`);
    process.exit(1);
  }
  const schema = JSON.parse(fs.readFileSync(schemaPath, "utf8"));
  const files = fs
    .readdirSync(pluginsDir)
    .filter((f) => f.endsWith(".json") && f !== "schema.json")
    .sort();

  if (files.length === 0) {
    console.error(`Error: no plugin manifests found in ${pluginsDir}`);
    process.exit(1);
  }

  const seenIds = new Map();
  let passed = 0;
  let failed = 0;

  for (const file of files) {
    const full = path.join(pluginsDir, file);
    let data;
    try {
      data = JSON.parse(fs.readFileSync(full, "utf8"));
    } catch (err) {
      failed++;
      console.error(`FAIL  ${file}: invalid JSON (${err.message})`);
      continue;
    }

    const errors = validateManifest(data, schema);
    if (data.id && seenIds.has(data.id)) {
      errors.push(`duplicate id "${data.id}" (already in ${seenIds.get(data.id)})`);
    } else if (data.id) {
      seenIds.set(data.id, file);
    }

    if (errors.length > 0) {
      failed++;
      console.error(`FAIL  ${file}: ${errors.join("; ")}`);
    } else {
      passed++;
      console.log(`PASS  ${file} (${data.id} v${data.version})`);
    }
  }

  console.log(`\nPlugin Validation: ${passed}/${files.length} PASS`);
  if (failed > 0) {
    process.exit(1);
  }
}

main();
