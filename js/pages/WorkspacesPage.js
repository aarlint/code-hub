import { defineComponent, h, ref, inject, computed, onUpdated, nextTick } from 'vue'
import StatsBar from '../components/StatsBar.js'
import WorkspaceCard from '../components/WorkspaceCard.js'
import EmptyState from '../components/EmptyState.js'
import ConfirmDialog from '../components/ConfirmDialog.js'
import ClusterPickerDialog from '../dialogs/ClusterPickerDialog.js'

export default defineComponent({
  name: 'WorkspacesPage',
  setup() {
    const instances = inject('instances')
    const globalStats = inject('globalStats')
    const clusters = inject('clusters')
    const pendingActions = inject('pendingActions')
    const api = inject('api')
    const { toast } = inject('toast')
    const wsConnected = inject('wsConnected')

    const confirmTarget = ref(null) // { id, name }
    const showClusterPicker = ref(null) // workspace type string
    const creatingType = ref(null) // 'vscode' | 'ai-code' while creating

    onUpdated(() => nextTick(() => lucide.createIcons()))

    function setPending(id, label) {
      pendingActions.value = { ...pendingActions.value, [id]: label }
    }

    function clearPending(id) {
      const copy = { ...pendingActions.value }
      delete copy[id]
      pendingActions.value = copy
    }

    async function refreshIfNeeded() {
      if (!wsConnected.value) {
        try {
          const data = await api.fetchInstances()
          instances.value = data.instances
          globalStats.value = data.global
          if (data.clusters) clusters.value = data.clusters
        } catch {}
      }
    }

    function createWorkspace(type) {
      const runningClusters = clusters.value.filter(c => c.status === 'running')
      if (runningClusters.length === 0) {
        doCreate(type, '')
      } else {
        showClusterPicker.value = type
      }
    }

    async function doCreate(type, cluster) {
      creatingType.value = type
      try {
        const inst = await api.createInstance(type, cluster)
        const label = type === 'ai-code' ? 'AI Code' : 'VS Code'
        const suffix = cluster ? ` (connected to ${cluster})` : ''
        toast(`${label} created: ${inst.name}${suffix}`, 'success')
        await refreshIfNeeded()
      } catch (e) {
        toast('Failed to create: ' + e.message, 'error')
      } finally {
        creatingType.value = null
      }
    }

    async function startWorkspace(id, name) {
      setPending(id, 'Starting...')
      try {
        await api.startInstance(id)
        toast('Workspace started: ' + name, 'success')
        await refreshIfNeeded()
      } catch (e) {
        toast('Failed to start: ' + e.message, 'error')
      }
      clearPending(id)
    }

    async function stopWorkspace(id, name) {
      setPending(id, 'Stopping...')
      try {
        await api.stopInstance(id)
        toast('Workspace stopped: ' + name, 'success')
        await refreshIfNeeded()
      } catch (e) {
        toast('Failed to stop: ' + e.message, 'error')
      }
      clearPending(id)
    }

    async function terminateWorkspace() {
      if (!confirmTarget.value) return
      const { id, name } = confirmTarget.value
      confirmTarget.value = null
      setPending(id, 'Terminating...')
      try {
        await api.terminateInstance(id)
        toast('Workspace terminated: ' + name, 'success')
        await refreshIfNeeded()
      } catch (e) {
        toast('Failed to terminate: ' + e.message, 'error')
      }
      clearPending(id)
    }

    const statsData = computed(() => {
      const insts = instances.value
      const g = globalStats.value
      const mine = insts.length
      const myRunning = insts.filter(i => i.state === 'running').length
      const myStopped = mine - myRunning
      const myVscode = insts.filter(i => (i.type || 'vscode') === 'vscode').length
      const myAiCode = insts.filter(i => i.type === 'ai-code').length
      const bt = g.byType || {}
      const gVscode = (bt.vscode || {}).total || 0
      const gAiCode = (bt['ai-code'] || {}).total || 0
      const fmt = (my, all) => my === all ? `${my}` : `${my} / ${all}`

      return [
        { icon: 'box', label: 'Total (yours / all)', value: fmt(mine, g.total) },
        { icon: 'code-2', iconColor: 'var(--accent)', label: 'VS Code', value: fmt(myVscode, gVscode), valueColor: 'var(--accent)' },
        { icon: 'terminal', iconColor: 'var(--accent-claude)', label: 'AI Code', value: fmt(myAiCode, gAiCode), valueColor: 'var(--accent-claude)' },
        { icon: 'play', iconColor: 'var(--accent-green)', label: 'Running', value: fmt(myRunning, g.running), valueColor: 'var(--accent-green)' },
        { icon: 'square', iconColor: 'var(--accent-red)', label: 'Stopped', value: fmt(myStopped, g.stopped), valueColor: 'var(--accent-red)' },
      ]
    })

    return () => {
      const children = [
        // Header
        h('div', { class: 'page-header' }, [
          h('div', { class: 'page-header__top' }, [
            h('div', [
              h('h1', { class: 'page-header__title' }, ['Workspaces ', h('span', '// dev environments')]),
              h('p', { class: 'page-header__sub' }, 'Manage your VS Code and AI Code instances'),
            ]),
            h('div', { class: 'page-header__actions' }, [
              h('button', {
                class: 'btn btn--create btn--primary',
                disabled: creatingType.value === 'vscode',
                onClick: () => createWorkspace('vscode'),
                innerHTML: creatingType.value === 'vscode'
                  ? '<span class="spinner"></span> Creating...'
                  : '<i data-lucide="code-2" style="width:14px;height:14px"></i> VS Code',
              }),
              h('button', {
                class: 'btn btn--create btn--claude',
                disabled: creatingType.value === 'ai-code',
                onClick: () => createWorkspace('ai-code'),
                innerHTML: creatingType.value === 'ai-code'
                  ? '<span class="spinner"></span> Creating...'
                  : '<i data-lucide="terminal" style="width:14px;height:14px"></i> AI Code',
              }),
            ]),
          ]),
        ]),

        // Stats
        h(StatsBar, { stats: statsData.value }),

        // Grid
        h('div', { class: 'section' }, [
          h('div', { class: 'section__title' }, 'Your Instances'),
          instances.value.length === 0
            ? h(EmptyState, {
                icon: 'code-2',
                title: 'No workspaces yet',
                subtitle: 'Create your first workspace to get started',
              }, {
                actions: () => [
                  h('button', {
                    class: 'btn btn--create btn--primary',
                    onClick: () => createWorkspace('vscode'),
                    innerHTML: '<i data-lucide="code-2" style="width:14px;height:14px"></i> VS Code',
                  }),
                  h('button', {
                    class: 'btn btn--create btn--claude',
                    onClick: () => createWorkspace('ai-code'),
                    innerHTML: '<i data-lucide="terminal" style="width:14px;height:14px"></i> AI Code',
                  }),
                ],
              })
            : h('div', { class: 'workspace-grid' },
                instances.value.map(inst =>
                  h(WorkspaceCard, {
                    key: inst.id,
                    instance: inst,
                    pending: pendingActions.value[inst.id] || null,
                    onStart: () => startWorkspace(inst.id, inst.name),
                    onStop: () => stopWorkspace(inst.id, inst.name),
                    onTerminate: () => confirmTarget.value = { id: inst.id, name: inst.name },
                  })
                )
              ),
        ]),
      ]

      // Confirm dialog
      if (confirmTarget.value) {
        children.push(h(ConfirmDialog, {
          title: 'Terminate Workspace',
          message: `Are you sure you want to terminate <code>${confirmTarget.value.name}</code>?<br>The container and all its data will be permanently deleted.`,
          confirmLabel: 'Terminate',
          confirmIcon: 'trash-2',
          onConfirm: terminateWorkspace,
          onCancel: () => confirmTarget.value = null,
        }))
      }

      // Cluster picker
      if (showClusterPicker.value) {
        const runningClusters = clusters.value.filter(c => c.status === 'running')
        children.push(h(ClusterPickerDialog, {
          type: showClusterPicker.value,
          clusters: runningClusters,
          onCreate: (cluster) => {
            const type = showClusterPicker.value
            showClusterPicker.value = null
            doCreate(type, cluster)
          },
          onCancel: () => showClusterPicker.value = null,
        }))
      }

      return h('div', children)
    }
  },
})
