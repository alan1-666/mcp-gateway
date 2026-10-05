import assert from "node:assert/strict";
import test from "node:test";
import { legacyInvitationDestination } from "../src/website-routing";

test("old invitations move to the workspace with only the invitation fragment", () => {
  assert.equal(
    legacyInvitationDestination("#invite=sample-token"),
    "/console/#invite=sample-token",
  );
  assert.equal(
    legacyInvitationDestination(
      "#invite=sample%2Btoken&next=https://external.example",
    ),
    "/console/#invite=sample%2Btoken",
  );
});
test("public anchors and empty invitations stay on the website", () => {
  for (const hash of [
    "",
    "#how-it-works",
    "#response-control",
    "#invite=",
    "#next=https://external.example",
  ]) {
    assert.equal(legacyInvitationDestination(hash), null);
  }
});
