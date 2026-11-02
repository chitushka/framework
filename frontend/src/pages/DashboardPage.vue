<template>
  <div class="row">
    <div v-for="stat in [{label:'Пользователи',value:'2 382',icon:'U'}, {label:'Заказы',value:'1 204',icon:'O'}, {label:'Конверсия',value:'4,7%',icon:'%'}]" :key="stat.label" class="col-sm-6 col-xl-4">
      <div class="card"><div class="card-body"><div class="row"><div class="col mt-0"><h5 class="card-title">{{ stat.label }}</h5></div><div class="col-auto"><span class="stat-icon">{{ stat.icon }}</span></div></div><h1 class="mt-1 mb-3">{{ stat.value }}</h1><div class="mb-0"><span class="text-success">+3,2%</span><span class="text-muted"> за месяц</span></div></div></div>
    </div>
  </div>
  <div class="card"><div class="card-header"><h5 class="card-title mb-0">Последняя активность</h5></div><div class="card-body"><p class="mb-0">Подключите API приложения и замените демонстрационные данные.</p></div></div>
</template>
