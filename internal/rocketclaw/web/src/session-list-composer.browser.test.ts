import { test } from "bun:test";
import { appScenario } from "./session-list-app_test";

test("actual App renders transcripts, timeline detail, attachments, queued work, agent switches and recorded authors", () => appScenario("composer"), 180_000);
