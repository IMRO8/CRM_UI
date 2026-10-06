<script setup>
import {computed} from 'vue';import {fieldAccess} from './domain.js';import {latestForm} from './metadata.js';
const p=defineProps(['state','user','tenantId','formId','record','version','readonly']);
const fields=computed(()=>(p.version?p.state.forms.find(f=>f.id===p.formId&&f.version===p.version):latestForm(p.state,p.formId))?.itemFields||[]);
const mode=k=>fieldAccess(p.state,p.user,p.tenantId,'item_'+k,p.formId);
</script>
<template><div v-if="fields.length" class="mt-3"><p class="config-heading">Item fields</p><template v-for="f in fields" :key="f.key"><v-text-field v-if="mode(f.key)!=='hidden'" :model-value="record.itemData?.[f.key]??''" @update:model-value="v=>{if(!record.itemData)record.itemData={};record.itemData[f.key]=v}" :label="f.label+(f.required?' *':' (optional for this item)')" :readonly="readonly||mode(f.key)!=='edit'" :type="f.type==='date'?'date':f.type==='number'?'number':'text'"/></template></div></template>
