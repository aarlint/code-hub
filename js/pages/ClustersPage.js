import { defineComponent, h, ref, inject, computed, onUpdated, nextTick } from 'vue'
import StatsBar from '../components/StatsBar.js'
import ClusterCard from '../components/ClusterCard.js'
import EmptyState from '../components/EmptyState.js'
import ConfirmDialog from '../components/ConfirmDialog.js'
import CreateClusterDialog from '../dialogs/CreateClusterDialog.js'

export default defineComponent({
  name: 'ClustersPage',
  setup() {
    const instances = inject('instances')
    const globalStats = inject('globalStats')
    const clusters = inject('clusters')
    const pendingActions = inject('pendingActions')
    const api = inject('api')
    const { toast } = inject('toast')
    const wsConnected = inject('wsConnected')
    const clusterIdleTimeout = inject('clusterIdleTimeout')
    const { openPanel } = inject('iframePanel')

    const confirmTarget = ref(null) // cluster name
    const showCreateDialog = ref(false)

    onUpdated(() => nextTick(() => lucide.createIcons()))

    function setPending(name, label) {
      pendingActions.value = { ...pendingActions.value, ['cluster-' + name]: label }
    }

    function clearPending(name) {
      const copy = { ...pendingActions.value }
      delete copy['cluster-' + name]
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

    async function startCluster(name) {
      setPending(name, 'Starting...')
      try {
        await api.startCluster(name)
        toast('Cluster started: ' + name, 'success')
        await refreshIfNeeded()
      } catch (e) {
        toast('Failed to start cluster: ' + e.message, 'error')
      }
      clearPending(name)
    }

    async function stopCluster(name) {
      setPending(name, 'Stopping...')
      try {
        await api.stopCluster(name)
        toast('Cluster stopped: ' + name, 'success')
        await refreshIfNeeded()
      } catch (e) {
        toast('Failed to stop cluster: ' + e.message, 'error')
      }
      clearPending(name)
    }

    async function deleteCluster() {
      if (!confirmTarget.value) return
      const name = confirmTarget.value
      confirmTarget.value = null
      setPending(name, 'Deleting...')
      try {
        await api.deleteCluster(name)
        toast('Cluster deleted: ' + name, 'success')
        await refreshIfNeeded()
      } catch (e) {
        toast('Failed to delete cluster: ' + e.message, 'error')
      }
      clearPending(name)
    }

    async function launchTerminal(name) {
      setPending(name, 'Launching terminal...')
      try {
        const data = await api.launchTerminal(name)
        toast('Terminal launched for ' + name, 'success')
        await refreshIfNeeded()
        openPanel(data.url, 'Terminal: ' + name)
      } catch (e) {
        toast('Failed to launch terminal: ' + e.message, 'error')
      }
      clearPending(name)
    }

    async function removeTerminal(name) {
      setPending(name, 'Removing terminal...')
      try {
        await api.removeTerminal(name)
        toast('Terminal removed for ' + name, 'success')
        await refreshIfNeeded()
      } catch (e) {
        toast('Failed to remove terminal: ' + e.message, 'error')
      }
      clearPending(name)
    }

    async function extendCluster(name) {
      try {
        await api.extendCluster(name)
        toast('Timer extended for ' + name, 'success')
      } catch (e) {
        toast('Failed to extend: ' + e.message, 'error')
      }
    }

    async function createCluster(opts) {
      showCreateDialog.value = false
      try {
        await api.createCluster(opts.name)
        toast('Cluster created: ' + opts.name, 'success')
        await refreshIfNeeded()
      } catch (e) {
        toast('Failed to create cluster: ' + e.message, 'error')
      }
    }

    const statsData = computed(() => {
      const cls = clusters.value
      const total = cls.length
      const running = cls.filter(c => c.status === 'running').length
      const terminals = cls.filter(c => c.terminalState === 'running').length
      return [
        { icon: 'server', label: 'Total Clusters', value: `${total}` },
        { icon: 'play', iconColor: 'var(--accent-green)', label: 'Running', value: `${running}`, valueColor: 'var(--accent-green)' },
        { icon: 'terminal', iconColor: 'var(--accent-green)', label: 'Terminals', value: `${terminals}`, valueColor: 'var(--accent-green)' },
      ]
    })

    return () => {
      const children = [
        // Header
        h('div', { class: 'page-header' }, [
          h('div', { class: 'page-header__top' }, [
            h('div', [
              h('h1', { class: 'page-header__title' }, ['Clusters ', h('span', '// vCluster')]),
              h('p', { class: 'page-header__sub' }, 'Manage virtual clusters and terminals'),
            ]),
            h('div', { class: 'page-header__actions' }, [
              h('button', {
                class: 'btn btn--create btn--k8s',
                onClick: () => showCreateDialog.value = true,
                innerHTML: '<i data-lucide="plus" style="width:14px;height:14px"></i> New Cluster',
              }),
            ]),
          ]),
        ]),

        // Stats
        h(StatsBar, { stats: statsData.value }),

        // Grid
        h('div', { class: 'section' }, [
          h('div', { class: 'section__title' }, 'Discovered Clusters'),
          clusters.value.length === 0
            ? h(EmptyState, {
                icon: 'server',
                title: 'No clusters found',
                subtitle: 'Create a virtual cluster to get started',
              }, {
                actions: () => [
                  h('button', {
                    class: 'btn btn--create btn--k8s',
                    onClick: () => showCreateDialog.value = true,
                    innerHTML: '<i data-lucide="plus" style="width:14px;height:14px"></i> New Cluster',
                  }),
                ],
              })
            : h('div', { class: 'workspace-grid' },
                clusters.value.map(cl =>
                  h(ClusterCard, {
                    key: cl.name,
                    cluster: cl,
                    pending: pendingActions.value['cluster-' + cl.name] || null,
                    idleTimeout: clusterIdleTimeout.value,
                    onStart: () => startCluster(cl.name),
                    onStop: () => stopCluster(cl.name),
                    onDelete: () => confirmTarget.value = cl.name,
                    onLaunchTerminal: () => launchTerminal(cl.name),
                    onRemoveTerminal: () => removeTerminal(cl.name),
                    onExtend: () => extendCluster(cl.name),
                  })
                )
              ),
        ]),
      ]

      // Confirm delete dialog
      if (confirmTarget.value) {
        children.push(h(ConfirmDialog, {
          title: 'Delete Cluster',
          message: `Are you sure you want to delete <code>${confirmTarget.value}</code>?<br>All cluster data, nodes, and associated terminal containers will be permanently removed.`,
          confirmLabel: 'Delete',
          confirmIcon: 'trash-2',
          onConfirm: deleteCluster,
          onCancel: () => confirmTarget.value = null,
        }))
      }

      // Create cluster dialog
      if (showCreateDialog.value) {
        children.push(h(CreateClusterDialog, {
          onCreate: createCluster,
          onCancel: () => showCreateDialog.value = false,
        }))
      }

      return h('div', children)
    }
  },
})
