// A response may only be consumed by the page generation that sent it.
export class PluginRequests {
  active = true;
  private generation = 0;
  private readonly controllers = new Set<AbortController>();

  suspend(): void {
    this.active = false;
    this.generation++;
    this.controllers.forEach(controller => controller.abort());
    this.controllers.clear();
  }

  resume(): void { this.active = true; }

  async json(url: string, options: RequestInit = {}): Promise<{ok: boolean; status: number; data: unknown}> {
    if (!this.active) throw new Error('页面或登录状态已变化，请重新打开后核对。');
    const generation = this.generation, controller = new AbortController();
    this.controllers.add(controller);
    const timer = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await fetch(url, {...options, credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: controller.signal});
      const data: unknown = await response.json().catch(() => null);
      if (!this.active || generation !== this.generation || controller.signal.aborted) throw new Error('请求已取消，结果待核对。');
      return {ok: response.ok, status: response.status, data};
    } finally {
      clearTimeout(timer);
      this.controllers.delete(controller);
    }
  }
}
