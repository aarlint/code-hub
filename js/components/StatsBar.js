import { defineComponent, h, onMounted, nextTick } from 'vue'

export default defineComponent({
  name: 'StatsBar',
  props: {
    stats: { type: Array, required: true },
    // Each stat: { icon, iconColor?, label, value, valueColor? }
  },
  setup(props) {
    onMounted(() => nextTick(() => lucide.createIcons()))
    return () => h('div', { class: 'stats' },
      props.stats.map(s =>
        h('div', { class: 'stat' }, [
          h('div', {
            class: 'stat__label',
            innerHTML: `<i data-lucide="${s.icon}" style="width:12px;height:12px${s.iconColor ? ';color:' + s.iconColor : ''}"></i> ${s.label}`,
          }),
          h('div', {
            class: 'stat__value',
            style: s.valueColor ? { color: s.valueColor } : undefined,
          }, s.value),
        ])
      )
    )
  },
})
