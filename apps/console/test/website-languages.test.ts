import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { chinese, english } from "../src/website-copy";
import { renderWebsite } from "../src/website-template";
import { languageFromPath, languagePreferenceKey, readLanguagePreference, saveLanguagePreference, websiteDestination } from "../src/website-routing";

const template = readFileSync(new URL("../index.html", import.meta.url), "utf8");
test("both static pages have complete copy, language metadata and safe rendering", () => {
  assert.deepEqual(Object.keys(chinese).sort(), Object.keys(english).sort());
  for (const [locale,lang,heading] of [["en","en","Give agents"],["cn","zh-CN","让智能体"]] as const) {
    const html=renderWebsite(template,locale);
    assert.ok(!html.includes("{{"), "unrendered placeholder");
    assert.ok(html.includes(`<html lang="${lang}">`));
    assert.ok(html.includes(`rel="canonical" href="https://rillgate.cn/${locale}/"`));
    assert.ok(html.includes(heading));
    assert.ok(html.includes(`data-language="${locale}" aria-current="page"`));
    assert.ok(html.includes('href="/console/"'));
    assert.ok(html.includes('hreflang="zh-CN"'));
  }
  assert.equal(renderWebsite("{{connectTitle}}", "en"), "Connect &amp; review");
  assert.throws(() => renderWebsite("{{missingCopy}}", "cn"), /Missing website message/);
});
test("explicit language addresses win over a remembered preference", () => {
  for (const path of ["/cn", "/cn/", "/cn/index.html"]) assert.equal(languageFromPath(path), "cn");
  for (const path of ["/en", "/en/", "/en/index.html"]) assert.equal(languageFromPath(path), "en");
  assert.equal(languageFromPath("/console/"),null);
  assert.equal(languageFromPath("/cn/missing"),null);
  assert.equal(websiteDestination({pathname:"/en/",search:"",hash:""},"cn"),null);
  assert.equal(websiteDestination({pathname:"/cn/",search:"",hash:""},"en"),null);
});
test("remembered language keeps section/query and cannot capture invitation links", () => {
  assert.equal(websiteDestination({pathname:"/",search:"?source=docs",hash:"#response-control"},"cn"),"/cn/?source=docs#response-control");
  assert.equal(websiteDestination({pathname:"/",search:"",hash:""},null),null);
  assert.equal(websiteDestination({pathname:"/",search:"?next=https://external.example",hash:"#invite=example%2Btoken"},"cn"),"/console/#invite=example%2Btoken");
  assert.equal(websiteDestination({pathname:"/cn/",search:"",hash:"#invite=example"},"en"),"/console/#invite=example");
});
test("invalid or unavailable storage does not break language links", () => {
  assert.equal(readLanguagePreference({getItem:()=>"https://external.example"}),null);
  assert.equal(readLanguagePreference({getItem:()=>{throw new Error("blocked");}}),null);
  assert.doesNotThrow(()=>saveLanguagePreference({setItem:()=>{throw new Error("blocked");}},"cn"));
  const saved=new Map<string,string>();
  saveLanguagePreference({setItem:(key,value)=>saved.set(key,value)},"cn");
  assert.equal(saved.get(languagePreferenceKey),"cn");
  assert.equal(readLanguagePreference({getItem:(key)=>saved.get(key)??null}),"cn");
});
