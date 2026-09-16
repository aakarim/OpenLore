import { api } from "../api";

test.each([
  { status: 401, aborted: false, notifications: 1 },
  { status: 401, aborted: true, notifications: 0 },
  { status: 404, aborted: false, notifications: 0 },
  { status: 500, aborted: false, notifications: 0 },
])(
  "aggregation failure $status, aborted=$aborted",
  async ({ status, aborted, notifications }) => {
    const controller = new AbortController();
    const listener = vi.fn();
    window.addEventListener("dashboard-auth-expired", listener);
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
      // Simulate an obsolete request completing after cancellation.
      if (aborted) controller.abort();
      return new Response("failure", { status });
    });
    try {
      await expect(
        api.aggregation("top-commands", "/", 7, controller.signal),
      ).rejects.toMatchObject({ status });
      expect(listener).toHaveBeenCalledTimes(notifications);
    } finally {
      window.removeEventListener("dashboard-auth-expired", listener);
    }
  },
);
