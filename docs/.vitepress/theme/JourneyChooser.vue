<script setup lang="ts">
import { nextTick, ref } from 'vue'
import { withBase } from 'vitepress'

const paths = [
  {
    label: 'Test and publish',
    eyebrow: '01 · Ship it',
    title: 'Create, Test and Publish before Deploy',
    summary: 'Start with a prepared runtime and inference, registry authentication, and kmx.yaml with publishRepository, destination selectors and evaluationTarget; create supplies none of these. Deploy freezes inputs and composes missing Test, Publish (registry push) and placement with source-only promotion.',
    command: 'kmx create … → kmx package deploy --to NAME',
    link: '/docs/getting-started#an-agent-of-your-own',
    action: 'Create and Deploy your agent'
  },
  {
    label: 'First answer',
    eyebrow: '02 · Try it',
    title: 'Start locally with a real answer',
    summary: 'Bring up kind, Ollama, and pinned Orka, then require a fresh Task to return a readable result.',
    command: 'kmx cluster quickstart',
    link: '/docs/getting-started#one-command-and-an-agent-that-answers',
    action: 'Open the local quickstart'
  },
  {
    label: 'Use an agent',
    eyebrow: '03 · Operate it',
    title: 'Run or chat with an existing Orka Agent',
    summary: 'Run sends one Task to an existing live Orka Agent, or chat opens a session; neither executes a workspace locally.',
    command: 'kmx run · kmx chat',
    link: '/docs/interactive-chat',
    action: 'Explore live interaction'
  },
  {
    label: 'Contribute',
    eyebrow: '04 · Extend it',
    title: 'Understand the boundaries before changing them',
    summary: 'Trace the command tree, runtime seams, safety invariants, and verification gates used by contributors.',
    command: 'make · go test ./...',
    link: '/docs/development',
    action: 'Read the developer guide'
  }
]

const active = ref(0)
const tabs = ref<HTMLButtonElement[]>([])

/** Select a journey and optionally move keyboard focus to its tab. */
function activate(index: number, focus = false) {
  active.value = index
  if (focus) nextTick(() => tabs.value[index]?.focus())
}

/** Apply the standard arrow, Home, and End behavior for an ARIA tablist. */
function move(event: KeyboardEvent, index: number) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  if (event.key === 'Home') return activate(0, true)
  if (event.key === 'End') return activate(paths.length - 1, true)
  const direction = event.key === 'ArrowRight' ? 1 : -1
  activate((index + direction + paths.length) % paths.length, true)
}
</script>

<template>
  <section class="journey" aria-labelledby="journey-title">
    <header class="journey__header">
      <p class="journey__kicker">Choose your path</p>
      <h2 id="journey-title">One builder, four useful starting points.</h2>
    </header>

    <div class="journey__shell">
      <div class="journey__tabs" role="tablist" aria-label="Documentation paths">
        <button
          v-for="(path, index) in paths"
          :id="`journey-tab-${index}`"
          :key="path.label"
          :ref="(element) => { if (element) tabs[index] = element as HTMLButtonElement }"
          class="journey__tab"
          :class="{ 'journey__tab--active': active === index }"
          type="button"
          role="tab"
          :aria-controls="`journey-panel-${index}`"
          :aria-selected="active === index"
          :tabindex="active === index ? 0 : -1"
          @click="activate(index)"
          @keydown="move($event, index)"
        >
          <span>{{ String(index + 1).padStart(2, '0') }}</span>
          {{ path.label }}
        </button>
      </div>

      <article
        v-for="(path, index) in paths"
        v-show="active === index"
        :id="`journey-panel-${index}`"
        :key="path.label"
        class="journey__panel"
        role="tabpanel"
        :aria-labelledby="`journey-tab-${index}`"
        :tabindex="active === index ? 0 : -1"
      >
        <div>
          <p class="journey__eyebrow">{{ path.eyebrow }}</p>
          <h3>{{ path.title }}</h3>
          <p>{{ path.summary }}</p>
        </div>
        <div class="journey__action">
          <code>{{ path.command }}</code>
          <a :href="withBase(path.link)">{{ path.action }} <span aria-hidden="true">↗</span></a>
        </div>
      </article>
    </div>
  </section>
</template>
