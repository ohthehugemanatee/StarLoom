<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import Section from 'picocrank/vue/components/Section.vue'
import FormField from 'picocrank/vue/components/FormField.vue'
import FormLayout from 'picocrank/vue/components/FormLayout.vue'
import RadioGroup from 'picocrank/vue/components/RadioGroup.vue'
import { HugeiconsIcon } from '@hugeicons/vue'
import { ArrowLeft01Icon, StarIcon } from '@hugeicons/core-free-icons'
import { starapp, type FamilyMember } from '../api/client'

const router = useRouter()
const error = ref('')
const creating = ref(false)
const people = ref<FamilyMember[]>([])

const form = reactive({
  name: '',
  sortOrder: 0,
  childMemberId: 0,
})

const chartOwnerOptions = computed(() => [
  { label: 'Everyone', value: 0 },
  ...people.value.map((m) => ({ label: m.displayName, value: m.id })),
])

async function loadPeople() {
  try {
    const res = await starapp.listMembers()
    people.value = res.members || []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function createChart() {
  creating.value = true
  error.value = ''
  try {
    const res = await starapp.createStarChart({
      name: form.name.trim(),
      sortOrder: form.sortOrder,
      childMemberId: form.childMemberId || undefined,
    })
    const id = res.starChart?.id
    if (id) {
      router.push({ name: 'familyStarChartEdit', params: { id } })
    } else {
      router.push({ name: 'familyStarCharts' })
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

onMounted(loadPeople)
</script>

<template>
  <Section title="Add star chart" :icon="StarIcon" :padding="true">
    <template #toolbar>
      <RouterLink :to="{ name: 'familyStarCharts' }" class="button inline-icon neutral">
        <HugeiconsIcon :icon="ArrowLeft01Icon" width="1em" height="1em" aria-hidden="true" />
        <span>Star Charts</span>
      </RouterLink>
    </template>

    <p class="subtle">Create a chart, then assign chores to it from the Chores page.</p>

    <FormLayout @submit.prevent="createChart">
      <p v-if="error" class="inline-notification error">{{ error }}</p>
      <FormField label="Name" for="star-chart-create-name">
        <input
          id="star-chart-create-name"
          v-model="form.name"
          type="text"
          required
          placeholder="Morning routine"
          :disabled="creating"
        />
      </FormField>
      <FormField label="For" component-has-label>
        <RadioGroup
          v-model="form.childMemberId"
          variant="list"
          :options="chartOwnerOptions"
          name="star-chart-create-child"
        />
        <small class="subtle">
          A chart for one person shows only their column, and every chore on it is theirs.
        </small>
      </FormField>
      <FormField label="Sort order" for="star-chart-create-sort">
        <input
          id="star-chart-create-sort"
          v-model.number="form.sortOrder"
          type="number"
          min="0"
          :disabled="creating"
        />
      </FormField>
      <template #actions>
        <button type="submit" class="good" :disabled="creating || !form.name.trim()">
          {{ creating ? 'Creating…' : 'Create' }}
        </button>
        <RouterLink :to="{ name: 'familyStarCharts' }" class="button neutral">Cancel</RouterLink>
      </template>
    </FormLayout>
  </Section>
</template>
