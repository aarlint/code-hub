import { defineComponent, h, Teleport, onMounted, nextTick } from 'vue'

export default defineComponent({
  name: 'ConfirmDialog',
  props: {
    title: { type: String, required: true },
    message: { type: String, required: true },
    confirmLabel: { type: String, default: 'Confirm' },
    confirmIcon: { type: String, default: 'trash-2' },
    danger: { type: Boolean, default: true },
  },
  emits: ['confirm', 'cancel'],
  setup(props, { emit }) {
    onMounted(() => nextTick(() => lucide.createIcons()))

    function onOverlayClick(e) {
      if (e.target === e.currentTarget) emit('cancel')
    }

    return () => h(Teleport, { to: 'body' },
      h('div', { class: 'confirm-overlay', onClick: onOverlayClick }, [
        h('div', { class: 'confirm-dialog' }, [
          h('div', { class: 'confirm-dialog__title' }, props.title),
          h('div', { class: 'confirm-dialog__msg', innerHTML: props.message }),
          h('div', { class: 'confirm-dialog__actions' }, [
            h('button', { class: 'btn', onClick: () => emit('cancel') }, 'Cancel'),
            h('button', {
              class: `btn ${props.danger ? 'btn--danger' : 'btn--primary'}`,
              onClick: () => emit('confirm'),
              innerHTML: `<i data-lucide="${props.confirmIcon}" style="width:14px;height:14px"></i> ${props.confirmLabel}`,
            }),
          ]),
        ]),
      ])
    )
  },
})
