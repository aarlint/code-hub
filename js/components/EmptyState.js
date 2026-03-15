import { defineComponent, h, onMounted, nextTick } from 'vue'

export default defineComponent({
  name: 'EmptyState',
  props: {
    icon: { type: String, default: 'code-2' },
    title: { type: String, required: true },
    subtitle: { type: String, default: '' },
  },
  setup(props, { slots }) {
    onMounted(() => nextTick(() => lucide.createIcons()))
    return () => h('div', { class: 'empty-state' }, [
      h('div', { class: 'empty-state__icon', innerHTML: `<i data-lucide="${props.icon}" style="width:48px;height:48px"></i>` }),
      h('div', { class: 'empty-state__title' }, props.title),
      props.subtitle && h('div', { class: 'empty-state__sub' }, props.subtitle),
      slots.actions && h('div', { class: 'empty-state__actions' }, slots.actions()),
    ])
  },
})
