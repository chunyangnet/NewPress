import { useEffect, useRef } from 'react';
import { init, use } from 'echarts/core';
import type { EChartsType, EChartsCoreOption } from 'echarts/core';
import { LineChart, BarChart } from 'echarts/charts';
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components';
import { SVGRenderer } from 'echarts/renderers';

use([LineChart, BarChart, GridComponent, TooltipComponent, LegendComponent, SVGRenderer]);
export type Palette = { brand: string; text: string; muted: string; line: string; surface: string; accent: string };
export function Chart({ option, label, height = 300 }: { option: (palette: Palette) => EChartsCoreOption; label: string; height?: number }) {
  const container = useRef<HTMLDivElement>(null);
  const chart = useRef<EChartsType | null>(null);
  const currentOption = useRef(option);
  currentOption.current = option;
  const update = useRef<() => void>(() => {});
  useEffect(() => {
    const element = container.current;
    if (!element) return;
    const instance = init(element, undefined, { renderer: 'svg' });
    chart.current = instance;
    const motion = window.matchMedia('(prefers-reduced-motion: reduce)');
    const paint = () => {
      if (instance.isDisposed()) return;
      const styles = getComputedStyle(element);
      // Resolve CSS variables/color-mix to browser RGB before passing them to zrender.
      const probe = document.createElement('span');
      element.append(probe);
      const color = (name: string, fallback: string) => {
        probe.style.color = styles.getPropertyValue(name).trim() || fallback;
        return getComputedStyle(probe).color;
      };
      const palette = { brand: color('--admin-brand', '#2563eb'), text: color('--analytics-ink', '#182536'),
        muted: color('--analytics-muted', '#67758a'), line: color('--analytics-line', '#e4e9f0'),
        surface: color('--analytics-surface', '#fff'), accent: color('--analytics-chart-secondary', '#4472c4') };
      probe.remove();
      instance.setOption({ ...currentOption.current(palette), animation: !motion.matches, animationDuration: 240, animationDurationUpdate: 180 });
    };
    let observer: ResizeObserver | undefined;
    const dispose = () => {
      observer?.disconnect();
      document.removeEventListener('chunnel:color-mode', paint);
      motion.removeEventListener('change', paint);
      update.current = () => {};
      chart.current = null;
      instance.dispose();
    };
    try {
      update.current = paint;
      paint();
      observer = new ResizeObserver(() => { if (!instance.isDisposed()) instance.resize(); });
      observer.observe(element);
      document.addEventListener('chunnel:color-mode', paint);
      motion.addEventListener('change', paint);
    } catch (error) { dispose(); throw error; }
    return dispose;
  }, []);
  useEffect(() => update.current(), [option]);
  return <div className="analytics-react-chart" ref={container} role="img" aria-label={label} style={{ height }} />;
}
