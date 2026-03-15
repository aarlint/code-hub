import { ref } from 'vue'

const toasts = ref([])
let nextId = 0

export function useToast() {
  function toast(msg, type = 'success') {
    const id = nextId++
    toasts.value.push({ id, msg, type })
    setTimeout(() => {
      const idx = toasts.value.findIndex(t => t.id === id)
      if (idx !== -1) toasts.value.splice(idx, 1)
    }, 4000)
  }

  return { toasts, toast }
}
