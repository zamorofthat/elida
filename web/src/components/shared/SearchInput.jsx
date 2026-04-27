import { IconSearch } from './Icons'

export function SearchInput({ value, onChange, placeholder = 'Search...' }) {
  return (
    <div class="search-input-wrapper">
      <IconSearch />
      <input
        type="text"
        class="search-input"
        value={value}
        onInput={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
    </div>
  )
}
