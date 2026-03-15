import { defineComponent, h, ref, Teleport, onMounted, nextTick } from 'vue'

export default defineComponent({
  name: 'CreateClusterDialog',
  emits: ['create', 'cancel'],
  setup(props, { emit }) {
    const name = ref('')
    const error = ref('')
    const creating = ref(false)

    const nameRe = /^[a-z][a-z0-9-]{0,19}$/

    onMounted(() => nextTick(() => {
      lucide.createIcons()
      document.getElementById('cluster-name-input')?.focus()
    }))

    function validate() {
      if (!nameRe.test(name.value.trim())) {
        error.value = 'Invalid name. Use lowercase letters, numbers, hyphens. Start with a letter.'
        return false
      }
      error.value = ''
      return true
    }

    function onSubmit() {
      if (!validate()) return
      creating.value = true
      emit('create', {
        name: name.value.trim(),
      })
    }

    function onKeydown(e) {
      if (e.key === 'Enter') onSubmit()
    }

    function onOverlayClick(e) {
      if (e.target === e.currentTarget) emit('cancel')
    }

    return () => h(Teleport, { to: 'body' },
      h('div', { class: 'confirm-overlay', onClick: onOverlayClick }, [
        h('div', { class: 'confirm-dialog', style: 'text-align:left;max-width:480px' }, [
          h('div', { class: 'confirm-dialog__title', style: 'text-align:center' }, 'Create Virtual Cluster'),

          h('div', { style: 'margin-bottom:16px' }, [
            h('label', { class: 'form-label', for: 'cluster-name-input' }, 'Cluster Name'),
            h('input', {
              class: 'form-input', id: 'cluster-name-input', type: 'text',
              placeholder: 'my-cluster', maxlength: 20, autocomplete: 'off', spellcheck: false,
              value: name.value,
              onInput: e => name.value = e.target.value,
              onKeydown,
            }),
            h('div', { class: 'form-hint' }, 'Lowercase letters, numbers, and hyphens. Must start with a letter.'),
            error.value && h('div', { class: 'form-error', style: 'display:block' }, error.value),
          ]),

          h('div', { class: 'confirm-dialog__actions', style: 'justify-content:flex-end' }, [
            h('button', { class: 'btn', onClick: () => emit('cancel') }, 'Cancel'),
            h('button', {
              class: 'btn btn--k8s',
              disabled: creating.value,
              onClick: onSubmit,
              innerHTML: creating.value
                ? '<span class="spinner"></span> Creating...'
                : '<i data-lucide="plus" style="width:14px;height:14px"></i> Create',
            }),
          ]),
        ]),
      ])
    )
  },
})
